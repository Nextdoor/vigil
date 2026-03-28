//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	kindClusterName = "vigil-e2e"
	helmReleaseName = "vigil-e2e"
	helmNamespace   = "vigil-system"
	testTaintKey    = "node.nextdoor.com/initializing"
)

var clientset *kubernetes.Clientset

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "E2E Suite")
}

var _ = BeforeSuite(func() {
	By("Creating KIND cluster")
	runCmd("kind", "create", "cluster", "--name", kindClusterName, "--wait", "60s")

	By("Building Docker image")
	runCmd("docker", "build", "-t", "vigil-controller:e2e", ".")

	By("Loading image into KIND")
	runCmd("kind", "load", "docker-image", "vigil-controller:e2e", "--name", kindClusterName)

	By("Installing Helm chart")
	runCmd("helm", "install", helmReleaseName, "charts/vigil-controller",
		"--namespace", helmNamespace,
		"--create-namespace",
		"--set", "image.repository=vigil-controller",
		"--set", "image.tag=e2e",
		"--set", "image.pullPolicy=Never",
		"--set", "controllerManager.leaderElection.enabled=false",
		"--set", "controllerManager.logLevel=debug",
		"--set", "replicaCount=1",
		"--wait",
		"--timeout", "2m",
	)

	By("Building Kubernetes client")
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		kubeconfig = clientcmd.RecommendedHomeFile
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	Expect(err).NotTo(HaveOccurred())
	clientset, err = kubernetes.NewForConfig(cfg)
	Expect(err).NotTo(HaveOccurred())
})

var _ = AfterSuite(func() {
	By("Deleting KIND cluster")
	cmd := exec.Command("kind", "delete", "cluster", "--name", kindClusterName)
	cmd.Stdout = GinkgoWriter
	cmd.Stderr = GinkgoWriter
	_ = cmd.Run() // best-effort cleanup
})

func runCmd(name string, args ...string) {
	cmd := exec.Command(name, args...)
	cmd.Dir = projectRoot()
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if err != nil {
		Fail(fmt.Sprintf("command %q failed: %v\n%s", name+" "+fmt.Sprint(args), err, out.String()))
	}
	GinkgoWriter.Printf("$ %s %v\n%s\n", name, args, out.String())
}

func projectRoot() string {
	dir, err := os.Getwd()
	Expect(err).NotTo(HaveOccurred())
	if _, err := os.Stat(dir + "/go.mod"); err == nil {
		return dir
	}
	return dir + "/../.."
}

func getDeploymentPodLogs(ctx context.Context) string {
	pods, err := clientset.CoreV1().Pods(helmNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=vigil-controller",
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(pods.Items).NotTo(BeEmpty(), "no controller pods found")

	var allLogs string
	for _, pod := range pods.Items {
		req := clientset.CoreV1().Pods(helmNamespace).GetLogs(pod.Name, &corev1.PodLogOptions{})
		logBytes, err := req.DoRaw(ctx)
		if err != nil {
			allLogs += fmt.Sprintf("--- %s: error getting logs: %v\n", pod.Name, err)
			continue
		}
		allLogs += fmt.Sprintf("--- %s ---\n%s\n", pod.Name, string(logBytes))
	}
	return allLogs
}

func taintNode(ctx context.Context, nodeName, key, effect string) {
	node, err := clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred())

	node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{
		Key:    key,
		Effect: corev1.TaintEffect(effect),
	})

	_, err = clientset.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	Expect(err).NotTo(HaveOccurred())
}

func removeTaint(ctx context.Context, nodeName, key string) {
	node, err := clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred())

	var filtered []corev1.Taint
	for _, t := range node.Spec.Taints {
		if t.Key != key {
			filtered = append(filtered, t)
		}
	}
	node.Spec.Taints = filtered

	_, err = clientset.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	Expect(err).NotTo(HaveOccurred())
}

func getNodeName(ctx context.Context) string {
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	Expect(err).NotTo(HaveOccurred())
	Expect(nodes.Items).NotTo(BeEmpty())
	return nodes.Items[0].Name
}

func getControllerRestartCount(ctx context.Context) int32 {
	pods, err := clientset.CoreV1().Pods(helmNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=vigil-controller",
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(pods.Items).NotTo(BeEmpty())

	for _, cs := range pods.Items[0].Status.ContainerStatuses {
		if cs.Name == "manager" {
			return cs.RestartCount
		}
	}
	return 0
}

func waitAndCheckNoRestarts(ctx context.Context, initialRestarts int32, duration time.Duration) {
	Consistently(func() int32 {
		return getControllerRestartCount(ctx)
	}).WithTimeout(duration).WithPolling(2 * time.Second).Should(Equal(initialRestarts),
		"controller pod should not restart")
}
