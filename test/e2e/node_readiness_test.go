//go:build e2e

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
			deploy, err := clientset.AppsV1().Deployments(helmNamespace).Get(ctx,
				helmReleaseName+"-vigil-controller-controller-manager", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			Expect(deploymentAvailable(deploy)).To(BeTrue(),
				"deployment should have Available condition")
			Expect(deploy.Status.ReadyReplicas).To(Equal(*deploy.Spec.Replicas),
				"all replicas should be ready")
		})

		It("should have healthy pods with no restarts", func() {
			pods, err := clientset.CoreV1().Pods(helmNamespace).List(ctx, metav1.ListOptions{
				LabelSelector: "app.kubernetes.io/name=vigil-controller",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(pods.Items).NotTo(BeEmpty())

			for _, pod := range pods.Items {
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
			svcs, err := clientset.CoreV1().Services(helmNamespace).List(ctx, metav1.ListOptions{
				LabelSelector: "app.kubernetes.io/name=vigil-controller",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(svcs.Items).NotTo(BeEmpty(), "should find at least one service")

			svcName := svcs.Items[0].Name
			endpoints, err := clientset.CoreV1().Endpoints(helmNamespace).Get(ctx, svcName, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			Expect(endpoints.Subsets).NotTo(BeEmpty(),
				"metrics service %s should have endpoints", svcName)
		})

		It("should have started the manager and node-readiness controller", func() {
			logs := getDeploymentPodLogs(ctx)
			Expect(logs).To(ContainSubstring("starting manager"),
				"logs should show manager started")
			Expect(logs).To(ContainSubstring("Starting workers"),
				"logs should show controller workers started")
		})
	})

	Context("daemonset inventory", func() {
		It("should log discovered DaemonSets on startup", func() {
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
			restarts := getControllerRestartCount(ctx)
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
				ContainSubstring("node has startup taint"),
				"controller should detect the startup taint")

			By("Verifying controller evaluates pod readiness")
			Eventually(func() string {
				return getDeploymentPodLogs(ctx)
			}).WithTimeout(30*time.Second).WithPolling(2*time.Second).Should(
				SatisfyAny(
					ContainSubstring("all expected DaemonSet pods are Ready"),
					ContainSubstring("waiting for DaemonSet pods to become Ready"),
				),
				"controller should evaluate pod readiness for expected DaemonSets")
		})

		It("should report all DaemonSets as Ready", func() {
			// KIND DaemonSets (kube-proxy, kindnet) are already running and Ready.
			Eventually(func() string {
				return getDeploymentPodLogs(ctx)
			}).WithTimeout(60*time.Second).WithPolling(2*time.Second).Should(
				ContainSubstring("all expected DaemonSet pods are Ready"),
				"controller should report all DaemonSet pods as Ready")
		})

		It("should emit expected and ready DaemonSet metrics", func() {
			metricsBody := getMetrics(ctx)

			Expect(metricsBody).To(ContainSubstring("vigil_expected_daemonsets"),
				"metrics should include vigil_expected_daemonsets gauge")
			Expect(metricsBody).To(ContainSubstring("vigil_ready_daemonsets"),
				"metrics should include vigil_ready_daemonsets gauge")
		})

		It("should not crash after evaluating readiness", func() {
			restarts := getControllerRestartCount(ctx)
			waitAndCheckNoRestarts(ctx, restarts, 10*time.Second)
		})

		AfterAll(func() {
			By("Cleaning up: removing taint from node")
			removeTaint(ctx, nodeName, testTaintKey)
			time.Sleep(2 * time.Second)
		})
	})

	Context("metrics endpoint", func() {
		It("should expose Prometheus metrics", func() {
			metricsBody := getMetrics(ctx)
			Expect(metricsBody).To(ContainSubstring("controller_runtime_reconcile_total"),
				"metrics should include controller-runtime reconcile metrics")
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
