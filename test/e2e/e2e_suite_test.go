//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

const (
	kindClusterName = "vigil-e2e"
	helmReleaseName = "vigil-e2e"
	helmNamespace   = "vigil-system"
	testTaintKey    = "node.nextdoor.com/initializing"
)

var (
	clientset  *kubernetes.Clientset
	restConfig *rest.Config
)

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "E2E Suite")
}

var _ = BeforeSuite(func() {
	// These exec calls are for cluster lifecycle only — no alternative exists
	// for kind/docker/helm CLI operations.
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
	var err error
	restConfig, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	Expect(err).NotTo(HaveOccurred())
	clientset, err = kubernetes.NewForConfig(restConfig)
	Expect(err).NotTo(HaveOccurred())
})

var _ = AfterSuite(func() {
	By("Deleting KIND cluster")
	cmd := exec.Command("kind", "delete", "cluster", "--name", kindClusterName)
	cmd.Stdout = GinkgoWriter
	cmd.Stderr = GinkgoWriter
	_ = cmd.Run() // best-effort cleanup
})

// runCmd executes a shell command for cluster lifecycle operations only.
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

// getDeploymentPodLogs reads logs from the controller pods via the Kubernetes API.
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

// taintNode adds a taint to the given node via the Kubernetes API.
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

// removeTaint removes a taint by key from the given node via the Kubernetes API.
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

// getNodeName returns the name of the first node in the cluster.
func getNodeName(ctx context.Context) string {
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	Expect(err).NotTo(HaveOccurred())
	Expect(nodes.Items).NotTo(BeEmpty())
	return nodes.Items[0].Name
}

// getControllerRestartCount returns the restart count of the controller container.
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

// waitAndCheckNoRestarts verifies the controller does not restart during the given duration.
func waitAndCheckNoRestarts(ctx context.Context, initialRestarts int32, duration time.Duration) {
	Consistently(func() int32 {
		return getControllerRestartCount(ctx)
	}).WithTimeout(duration).WithPolling(2*time.Second).Should(Equal(initialRestarts),
		"controller pod should not restart")
}

// getMetrics fetches the /metrics endpoint from the controller using port-forward.
func getMetrics(ctx context.Context) string {
	pods, err := clientset.CoreV1().Pods(helmNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=vigil-controller",
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(pods.Items).NotTo(BeEmpty())

	podName := pods.Items[0].Name

	// Set up port-forward to the controller pod's metrics port.
	reqURL := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(helmNamespace).
		Name(podName).
		SubResource("portforward").
		URL()

	transport, upgrader, err := spdy.RoundTripperFor(restConfig)
	Expect(err).NotTo(HaveOccurred())

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, reqURL)

	stopCh := make(chan struct{})
	readyCh := make(chan struct{})
	errOut := &bytes.Buffer{}

	// Use port 0 for local port to get a random available port.
	fw, err := portforward.New(dialer, []string{"0:8080"}, stopCh, readyCh, io.Discard, errOut)
	Expect(err).NotTo(HaveOccurred())

	go func() {
		defer GinkgoRecover()
		err := fw.ForwardPorts()
		if err != nil {
			GinkgoWriter.Printf("port-forward error: %v, stderr: %s\n", err, errOut.String())
		}
	}()

	// Wait for port-forward to be ready.
	select {
	case <-readyCh:
	case <-time.After(15 * time.Second):
		close(stopCh)
		Fail("timed out waiting for port-forward to be ready")
	}

	defer close(stopCh)

	ports, err := fw.GetPorts()
	Expect(err).NotTo(HaveOccurred())
	Expect(ports).NotTo(BeEmpty())
	localPort := ports[0].Local

	// Fetch metrics via HTTP.
	metricsURL := fmt.Sprintf("http://localhost:%d/metrics", localPort)
	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Get(metricsURL)
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	Expect(err).NotTo(HaveOccurred())
	Expect(resp.StatusCode).To(Equal(http.StatusOK))

	return string(body)
}
