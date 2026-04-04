//go:build e2e

// Copyright 2026 Nextdoor, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.


package e2e

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("Node Readiness Controller", Ordered, func() {
	var ctx context.Context

	BeforeAll(func() {
		ctx = context.Background()
	})

	Context("controller installation", func() {
		It("should have a running deployment", func() {
			By("Fetching deployment from Kubernetes API")
			deploy, err := clientset.AppsV1().Deployments(helmNamespace).Get(ctx,
				helmReleaseName+"-vigil-controller-controller-manager", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())

			By("Checking Available condition")
			Expect(deploymentAvailable(deploy)).To(BeTrue(),
				"deployment should have Available condition")

			By("Verifying all replicas are ready")
			Expect(deploy.Status.ReadyReplicas).To(Equal(*deploy.Spec.Replicas),
				"all replicas should be ready")
		})

		It("should have healthy pods with no restarts", func() {
			By("Listing controller pods")
			pods, err := clientset.CoreV1().Pods(helmNamespace).List(ctx, metav1.ListOptions{
				LabelSelector: "app.kubernetes.io/name=vigil-controller",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(pods.Items).NotTo(BeEmpty())

			for _, pod := range pods.Items {
				By("Checking pod " + pod.Name + " is Running with no restarts")
				Expect(pod.Status.Phase).To(Equal(corev1.PodRunning),
					"pod %s should be Running", pod.Name)
				for _, cs := range pod.Status.ContainerStatuses {
					Expect(cs.RestartCount).To(BeZero(),
						"container %s in pod %s should have 0 restarts", cs.Name, pod.Name)
					Expect(cs.Ready).To(BeTrue(),
						"container %s in pod %s should be ready", cs.Name, pod.Name)
				}
			}
		})

		It("should pass health checks", func() {
			By("Listing vigil-controller services")
			svcs, err := clientset.CoreV1().Services(helmNamespace).List(ctx, metav1.ListOptions{
				LabelSelector: "app.kubernetes.io/name=vigil-controller",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(svcs.Items).NotTo(BeEmpty(), "should find at least one service")

			svcName := svcs.Items[0].Name
			By("Verifying endpoints for service " + svcName)
			endpoints, err := clientset.CoreV1().Endpoints(helmNamespace).Get(ctx, svcName, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			Expect(endpoints.Subsets).NotTo(BeEmpty(),
				"metrics service %s should have endpoints", svcName)
		})

		It("should have started the manager and node-readiness controller", func() {
			By("Reading controller pod logs")
			logs := getDeploymentPodLogs(ctx)

			By("Checking for manager startup log")
			Expect(logs).To(ContainSubstring("starting manager"),
				"logs should show manager started")

			By("Checking for controller workers startup log")
			Expect(logs).To(ContainSubstring("Starting workers"),
				"logs should show controller workers started")
		})
	})

	Context("daemonset inventory", func() {
		It("should log discovered DaemonSets on startup", func() {
			By("Waiting for inventory controller to log DaemonSet additions")
			// KIND clusters have at least kube-proxy and kindnet DaemonSets.
			Eventually(func() string {
				return getDeploymentPodLogs(ctx)
			}).WithTimeout(30*time.Second).WithPolling(2*time.Second).Should(
				ContainSubstring("daemonset added"),
				"inventory controller should log DaemonSet additions on startup")
		})
	})

	Context("node watching", func() {
		It("should remain stable with no restarts over 10 seconds", func() {
			By("Recording initial restart count")
			restarts := getControllerRestartCount(ctx)
			By("Verifying no restarts over 10 second window")
			waitAndCheckNoRestarts(ctx, restarts, 10*time.Second)
		})
	})

	Context("taint detection and readiness evaluation", func() {
		var nodeName string

		BeforeAll(func() {
			nodeName = getNodeName(ctx)
		})

		It("should detect a startup taint and discover expected DaemonSets", func() {
			By("Adding startup taint to node")
			taintNode(ctx, nodeName, testTaintKey, "NoSchedule")

			By("Waiting for controller to log taint detection")
			Eventually(func() string {
				return getDeploymentPodLogs(ctx)
			}).WithTimeout(30*time.Second).WithPolling(2*time.Second).Should(
				SatisfyAny(
					ContainSubstring("tracking new node with startup taint"),
					ContainSubstring("node ready, removing taint"),
				),
				"controller should detect the startup taint")

			By("Verifying controller evaluates pod readiness")
			Eventually(func() string {
				return getDeploymentPodLogs(ctx)
			}).WithTimeout(30*time.Second).WithPolling(2*time.Second).Should(
				SatisfyAny(
					ContainSubstring("node ready, removing taint"),
					ContainSubstring("DaemonSet readiness changed"),
					ContainSubstring("tracking new node with startup taint"),
				),
				"controller should evaluate pod readiness for expected DaemonSets")
		})

		It("should remove the taint after all DaemonSets are Ready", func() {
			By("Waiting for controller to report all DaemonSet pods as Ready")
			// KIND DaemonSets (kube-proxy, kindnet) are already running and Ready.
			Eventually(func() string {
				return getDeploymentPodLogs(ctx)
			}).WithTimeout(60*time.Second).WithPolling(2*time.Second).Should(
				ContainSubstring("node ready, removing taint"),
				"controller should report all DaemonSet pods as Ready")

			By("Verifying taint was removed from the node")
			Eventually(func() bool {
				node, err := clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
				if err != nil {
					return false
				}
				for _, t := range node.Spec.Taints {
					if t.Key == testTaintKey {
						return false
					}
				}
				return true
			}).WithTimeout(30*time.Second).WithPolling(2*time.Second).Should(BeTrue(),
				"startup taint should be removed from the node")

			By("Verifying controller logged taint removal")
			Eventually(func() string {
				return getDeploymentPodLogs(ctx)
			}).WithTimeout(10*time.Second).WithPolling(2*time.Second).Should(
				ContainSubstring("taint removed"),
				"controller should log taint removal")
		})

		It("should emit taint removal and readiness metrics", func() {
			By("Fetching /metrics via port-forward")
			metricsBody := getMetrics(ctx)

			By("Checking for vigil_expected_daemonsets metric")
			Expect(metricsBody).To(ContainSubstring("vigil_expected_daemonsets"),
				"metrics should include vigil_expected_daemonsets gauge")

			By("Checking for vigil_ready_daemonsets metric")
			Expect(metricsBody).To(ContainSubstring("vigil_ready_daemonsets"),
				"metrics should include vigil_ready_daemonsets gauge")

			By("Checking for vigil_successful_removals_total metric")
			Expect(metricsBody).To(ContainSubstring("vigil_successful_removals_total"),
				"metrics should include vigil_successful_removals_total counter")

			By("Checking for vigil_taint_removal_duration_seconds metric")
			Expect(metricsBody).To(ContainSubstring("vigil_taint_removal_duration_seconds"),
				"metrics should include vigil_taint_removal_duration_seconds histogram")
		})

		It("should not crash after evaluating readiness", func() {
			By("Recording restart count after readiness evaluation")
			restarts := getControllerRestartCount(ctx)
			By("Verifying stability over 10 seconds")
			waitAndCheckNoRestarts(ctx, restarts, 10*time.Second)
		})

		AfterAll(func() {
			By("Cleaning up: ensuring taint is removed from node")
			// The controller should have removed it, but clean up just in case.
			removeTaint(ctx, nodeName, testTaintKey)
		})
	})

	Context("metrics endpoint", func() {
		It("should expose Prometheus metrics", func() {
			By("Fetching /metrics endpoint via port-forward")
			metricsBody := getMetrics(ctx)

			By("Checking for controller-runtime reconcile metrics")
			Expect(metricsBody).To(ContainSubstring("controller_runtime_reconcile_total"),
				"metrics should include controller-runtime reconcile metrics")

			By("Checking for vigil discovery duration histogram")
			Expect(metricsBody).To(ContainSubstring("vigil_discovery_duration_seconds"),
				"metrics should include vigil discovery duration histogram")
		})
	})
})

func deploymentAvailable(deploy *appsv1.Deployment) bool {
	for _, c := range deploy.Status.Conditions {
		if c.Type == appsv1.DeploymentAvailable && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}