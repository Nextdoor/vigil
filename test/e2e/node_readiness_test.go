//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os/exec"
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
			// Find the metrics service by label selector
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

	Context("node watching", func() {
		It("should remain stable with no restarts over 10 seconds", func() {
			restarts := getControllerRestartCount(ctx)
			waitAndCheckNoRestarts(ctx, restarts, 10*time.Second)
		})
	})

	Context("taint detection", func() {
		var nodeName string

		BeforeAll(func() {
			nodeName = getNodeName(ctx)
		})

		It("should detect a startup taint on a node", func() {
			By("Adding startup taint to node")
			taintNode(ctx, nodeName, testTaintKey, "NoSchedule")

			By("Waiting for controller to log detection")
			Eventually(func() string {
				return getDeploymentPodLogs(ctx)
			}).WithTimeout(30 * time.Second).WithPolling(2 * time.Second).Should(
				ContainSubstring("node has startup taint"),
				"controller should detect the startup taint")
		})

		It("should not crash after detecting a tainted node", func() {
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
			// Run a curl pod to hit the metrics service from inside the cluster
			svcs, err := clientset.CoreV1().Services(helmNamespace).List(ctx, metav1.ListOptions{
				LabelSelector: "app.kubernetes.io/name=vigil-controller",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(svcs.Items).NotTo(BeEmpty())

			svcName := svcs.Items[0].Name
			metricsURL := fmt.Sprintf("http://%s.%s.svc:8080/metrics", svcName, helmNamespace)

			Eventually(func() string {
				cmd := exec.Command("kubectl", "run", "curl-test", "--rm", "-i",
					"--restart=Never", "--image=curlimages/curl:8.5.0",
					"-n", helmNamespace,
					"--", "curl", "-sf", "--max-time", "5", metricsURL)
				out, _ := cmd.CombinedOutput()
				// Clean up in case the pod lingers
				_ = exec.Command("kubectl", "delete", "pod", "curl-test",
					"-n", helmNamespace, "--ignore-not-found").Run()
				return string(out)
			}).WithTimeout(60 * time.Second).WithPolling(5 * time.Second).Should(
				ContainSubstring("controller_runtime_reconcile_total"),
				"metrics should include controller-runtime reconcile metrics")
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
