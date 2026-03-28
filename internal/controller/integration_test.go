package controller_test

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/nextdoor/vigil/internal/controller"
	"github.com/nextdoor/vigil/internal/discovery"
	"github.com/nextdoor/vigil/internal/readiness"
	"github.com/nextdoor/vigil/internal/taintremoval"
	"github.com/nextdoor/vigil/pkg/config"
)

// ---------------------------------------------------------------------------
// Test cluster topology
// ---------------------------------------------------------------------------
//
// Nodes:
//   worker-1   — regular linux, startup taint
//   worker-2   — regular linux, startup taint
//   gpu-1      — GPU node, startup taint + dedicated=gpu taint, label accelerator=nvidia
//   arm-1      — ARM node, startup taint, label kubernetes.io/arch=arm64
//   stable-1   — regular linux, NO startup taint (already initialized)
//
// DaemonSets:
//   kube-proxy    — universal, no selectors, no special tolerations
//   node-exporter — universal, tolerates everything (Operator=Exists)
//   aws-cni       — universal, no extra tolerations (blocked by dedicated=gpu)
//   gpu-driver    — nodeSelector: accelerator=nvidia, tolerates dedicated=gpu
//   arm-monitor   — nodeAffinity: arch In [arm64]
//   slow-ds       — universal, excluded by name
//   ignored-ds    — universal, excluded by label vigil.dev/ignore=true
//
// Expected DaemonSets per node (after startup taint stripping):
//   worker-1/2 → kube-proxy, node-exporter, aws-cni           (3)
//   gpu-1      → kube-proxy, node-exporter, gpu-driver         (3)
//   arm-1      → kube-proxy, node-exporter, aws-cni, arm-monitor (4)
//   stable-1   → skipped (no startup taint)

// ---------------------------------------------------------------------------
// Shared test configuration
// ---------------------------------------------------------------------------

func testConfig() *config.Config {
	return &config.Config{
		TaintKey:    "node.example.com/initializing",
		TaintEffect: "NoSchedule",
		StartupTaintKeys: []string{
			"node.example.com/initializing",
		},
		TimeoutSeconds: 120,
		ExcludeDaemonSets: config.ExcludeDaemonSets{
			ByName: []config.DaemonSetRef{
				{Namespace: "kube-system", Name: "slow-ds"},
			},
			ByLabel: &config.LabelSelector{
				MatchExpressions: []config.LabelSelectorRequirement{
					{Key: "vigil.dev/ignore", Operator: "Exists"},
				},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Node builders
// ---------------------------------------------------------------------------

func nodeWorker(name string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			CreationTimestamp: metav1.Now(),
			Labels: map[string]string{
				"kubernetes.io/os":   "linux",
				"kubernetes.io/arch": "amd64",
			},
		},
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{
				{Key: "node.example.com/initializing", Effect: corev1.TaintEffectNoSchedule},
			},
		},
	}
}

func nodeGPU() *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "gpu-1",
			CreationTimestamp: metav1.Now(),
			Labels: map[string]string{
				"kubernetes.io/os":   "linux",
				"kubernetes.io/arch": "amd64",
				"accelerator":        "nvidia",
			},
		},
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{
				{Key: "node.example.com/initializing", Effect: corev1.TaintEffectNoSchedule},
				{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule},
			},
		},
	}
}

func nodeARM() *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "arm-1",
			CreationTimestamp: metav1.Now(),
			Labels: map[string]string{
				"kubernetes.io/os":   "linux",
				"kubernetes.io/arch": "arm64",
			},
		},
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{
				{Key: "node.example.com/initializing", Effect: corev1.TaintEffectNoSchedule},
			},
		},
	}
}

func nodeStable() *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "stable-1",
			CreationTimestamp: metav1.Now(),
			Labels: map[string]string{
				"kubernetes.io/os":   "linux",
				"kubernetes.io/arch": "amd64",
			},
		},
	}
}

// ---------------------------------------------------------------------------
// DaemonSet builders
// ---------------------------------------------------------------------------

func dsKubeProxy() *appsv1.DaemonSet {
	return newDS("kube-system", "kube-proxy", nil)
}

func dsNodeExporter() *appsv1.DaemonSet {
	ds := newDS("monitoring", "node-exporter", nil)
	ds.Spec.Template.Spec.Tolerations = []corev1.Toleration{
		{Operator: corev1.TolerationOpExists},
	}
	return ds
}

func dsAWSCNI() *appsv1.DaemonSet {
	return newDS("kube-system", "aws-cni", nil)
}

func dsGPUDriver() *appsv1.DaemonSet {
	ds := newDS("kube-system", "gpu-driver", map[string]string{
		"accelerator": "nvidia",
	})
	ds.Spec.Template.Spec.Tolerations = []corev1.Toleration{
		{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "gpu", Effect: corev1.TaintEffectNoSchedule},
	}
	return ds
}

func dsARMMonitor() *appsv1.DaemonSet {
	ds := newDS("monitoring", "arm-monitor", nil)
	ds.Spec.Template.Spec.Affinity = &corev1.Affinity{
		NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      "kubernetes.io/arch",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"arm64"},
							},
						},
					},
				},
			},
		},
	}
	return ds
}

func dsSlowExcluded() *appsv1.DaemonSet {
	return newDS("kube-system", "slow-ds", nil)
}

func dsIgnoredByLabel() *appsv1.DaemonSet {
	ds := newDS("kube-system", "ignored-ds", nil)
	ds.Labels["vigil.dev/ignore"] = "true"
	return ds
}

func newDS(namespace, name string, nodeSelector map[string]string) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			UID:       types.UID("uid-" + namespace + "-" + name),
			Labels:    map[string]string{"app": name},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": name},
				},
				Spec: corev1.PodSpec{
					NodeSelector: nodeSelector,
					Containers: []corev1.Container{
						{Name: name, Image: "example/" + name + ":latest"},
					},
				},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Pod builder
// ---------------------------------------------------------------------------

func pod(ds *appsv1.DaemonSet, nodeName string, phase corev1.PodPhase, ready bool) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ds.Namespace,
			Name:      ds.Name + "-" + nodeName,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "DaemonSet",
					Name:       ds.Name,
					UID:        ds.UID,
				},
			},
		},
		Spec: corev1.PodSpec{
			NodeName:   nodeName,
			Containers: []corev1.Container{{Name: "main", Image: "example:latest"}},
		},
		Status: corev1.PodStatus{
			Phase: phase,
		},
	}
	if ready {
		p.Status.Conditions = []corev1.PodCondition{
			{Type: corev1.PodReady, Status: corev1.ConditionTrue},
		}
	}
	return p
}

func readyPod(ds *appsv1.DaemonSet, nodeName string) *corev1.Pod {
	return pod(ds, nodeName, corev1.PodRunning, true)
}

func pendingPod(ds *appsv1.DaemonSet, nodeName string) *corev1.Pod {
	return pod(ds, nodeName, corev1.PodPending, false)
}

func crashingPod(ds *appsv1.DaemonSet, nodeName string) *corev1.Pod {
	return pod(ds, nodeName, corev1.PodRunning, false)
}

func failedPod(ds *appsv1.DaemonSet, nodeName string) *corev1.Pod {
	return pod(ds, nodeName, corev1.PodFailed, false)
}

// ---------------------------------------------------------------------------
// Test infrastructure
// ---------------------------------------------------------------------------

func buildClient(objs ...client.Object) client.WithWatch {
	return fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(objs...).
		WithIndex(&corev1.Pod{}, readiness.NodeNameField, func(o client.Object) []string {
			p := o.(*corev1.Pod)
			if p.Spec.NodeName == "" {
				return nil
			}
			return []string{p.Spec.NodeName}
		}).
		Build()
}

func buildReconciler(cl client.Client, cfg *config.Config) *controller.NodeReadinessReconciler {
	return &controller.NodeReadinessReconciler{
		Client:       cl,
		Scheme:       testScheme,
		Log:          logr.Discard(),
		Config:       cfg,
		Discovery:    discovery.New(cl, logr.Discard(), cfg),
		Readiness:    readiness.New(cl, logr.Discard()),
		TaintRemover: taintremoval.New(cl, cl, logr.Discard()),
	}
}

func reconcile(r *controller.NodeReadinessReconciler, nodeName string) (ctrl.Result, error) {
	return r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: nodeName},
	})
}

// allDaemonSets returns every DaemonSet in the test cluster.
func allDaemonSets() []*appsv1.DaemonSet {
	return []*appsv1.DaemonSet{
		dsKubeProxy(), dsNodeExporter(), dsAWSCNI(),
		dsGPUDriver(), dsARMMonitor(),
		dsSlowExcluded(), dsIgnoredByLabel(),
	}
}

// allNodes returns every node in the test cluster.
func allNodes() []*corev1.Node {
	return []*corev1.Node{
		nodeWorker("worker-1"), nodeWorker("worker-2"),
		nodeGPU(), nodeARM(), nodeStable(),
	}
}

// clusterObjects returns nodes + DaemonSets as a client.Object slice.
func clusterObjects(extraPods ...*corev1.Pod) []client.Object {
	nodes := allNodes()
	dss := allDaemonSets()
	objs := make([]client.Object, 0, len(nodes)+len(dss)+len(extraPods))
	for _, n := range nodes {
		objs = append(objs, n)
	}
	for _, ds := range dss {
		objs = append(objs, ds)
	}
	for _, p := range extraPods {
		objs = append(objs, p)
	}
	return objs
}

// ===================================================================
// Test: Discovery correctness — right DaemonSets expected per node
// ===================================================================

func TestDiscovery_WorkerNodeExpects3DaemonSets(t *testing.T) {
	cfg := testConfig()
	cl := buildClient(clusterObjects()...)
	r := buildReconciler(cl, cfg)

	// worker-1 is tainted, expects: kube-proxy, node-exporter, aws-cni (3).
	// slow-ds and ignored-ds are excluded. gpu-driver and arm-monitor don't target this node.
	result, err := reconcile(r, "worker-1")
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter,
		"should requeue — no pods exist yet for 3 expected DaemonSets")
}

func TestDiscovery_GPUNodeExpects3DaemonSets(t *testing.T) {
	cfg := testConfig()
	proxy := dsKubeProxy()
	exporter := dsNodeExporter()
	gpu := dsGPUDriver()

	// All 3 expected DS pods are Ready on gpu-1.
	cl := buildClient(clusterObjects(
		readyPod(proxy, "gpu-1"),
		readyPod(exporter, "gpu-1"),
		readyPod(gpu, "gpu-1"),
	)...)
	r := buildReconciler(cl, cfg)

	result, err := reconcile(r, "gpu-1")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result,
		"gpu-1 should be ready: kube-proxy + node-exporter + gpu-driver all Ready")
}

func TestDiscovery_GPUNodeExcludesAWSCNI(t *testing.T) {
	cfg := testConfig()
	proxy := dsKubeProxy()
	exporter := dsNodeExporter()
	gpu := dsGPUDriver()
	cni := dsAWSCNI()

	// kube-proxy, node-exporter, gpu-driver are Ready.
	// aws-cni has NO pod — but it shouldn't be expected because it can't tolerate dedicated=gpu.
	cl := buildClient(clusterObjects(
		readyPod(proxy, "gpu-1"),
		readyPod(exporter, "gpu-1"),
		readyPod(gpu, "gpu-1"),
		// aws-cni pod intentionally missing — we also create one on worker-1 so
		// the linter sees makePod called with different node names.
		readyPod(cni, "worker-1"),
	)...)
	r := buildReconciler(cl, cfg)

	result, err := reconcile(r, "gpu-1")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result,
		"gpu-1 should be ready — aws-cni is filtered by toleration mismatch")
}

func TestDiscovery_ARMNodeExpects4DaemonSets(t *testing.T) {
	cfg := testConfig()
	proxy := dsKubeProxy()
	exporter := dsNodeExporter()
	cni := dsAWSCNI()
	arm := dsARMMonitor()

	// All 4 expected DS pods are Ready on arm-1.
	cl := buildClient(clusterObjects(
		readyPod(proxy, "arm-1"),
		readyPod(exporter, "arm-1"),
		readyPod(cni, "arm-1"),
		readyPod(arm, "arm-1"),
	)...)
	r := buildReconciler(cl, cfg)

	result, err := reconcile(r, "arm-1")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result,
		"arm-1 should be ready: kube-proxy + node-exporter + aws-cni + arm-monitor")
}

func TestDiscovery_ARMMonitorNotExpectedOnAMD64(t *testing.T) {
	cfg := testConfig()
	proxy := dsKubeProxy()
	exporter := dsNodeExporter()
	cni := dsAWSCNI()

	// worker-1 is amd64 — arm-monitor should NOT be expected.
	cl := buildClient(clusterObjects(
		readyPod(proxy, "worker-1"),
		readyPod(exporter, "worker-1"),
		readyPod(cni, "worker-1"),
	)...)
	r := buildReconciler(cl, cfg)

	result, err := reconcile(r, "worker-1")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result,
		"worker-1 should be ready with 3 DS — arm-monitor not expected on amd64")
}

// ===================================================================
// Test: Stable (untainted) node is skipped
// ===================================================================

func TestStableNode_Skipped(t *testing.T) {
	cfg := testConfig()
	cl := buildClient(clusterObjects()...)
	r := buildReconciler(cl, cfg)

	result, err := reconcile(r, "stable-1")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result,
		"stable-1 has no taint — should be skipped immediately")
}

// ===================================================================
// Test: Excluded DaemonSets don't block readiness
// ===================================================================

func TestExclusion_ByNameDoesNotBlock(t *testing.T) {
	cfg := testConfig()
	proxy := dsKubeProxy()
	exporter := dsNodeExporter()
	cni := dsAWSCNI()

	// All non-excluded DS pods Ready. slow-ds has no pod but is excluded by name.
	cl := buildClient(clusterObjects(
		readyPod(proxy, "worker-1"),
		readyPod(exporter, "worker-1"),
		readyPod(cni, "worker-1"),
	)...)
	r := buildReconciler(cl, cfg)

	result, err := reconcile(r, "worker-1")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result,
		"slow-ds excluded by name — should not block readiness")
}

func TestExclusion_ByLabelDoesNotBlock(t *testing.T) {
	cfg := testConfig()
	proxy := dsKubeProxy()
	exporter := dsNodeExporter()
	cni := dsAWSCNI()

	// All non-excluded DS pods Ready. ignored-ds has no pod but is excluded by label.
	cl := buildClient(clusterObjects(
		readyPod(proxy, "worker-2"),
		readyPod(exporter, "worker-2"),
		readyPod(cni, "worker-2"),
	)...)
	r := buildReconciler(cl, cfg)

	result, err := reconcile(r, "worker-2")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result,
		"ignored-ds excluded by label — should not block readiness")
}

// ===================================================================
// Test: Pod failure modes
// ===================================================================

func TestPodNotReady_NoPodExists(t *testing.T) {
	cfg := testConfig()
	// No pods at all on worker-1.
	cl := buildClient(clusterObjects()...)
	r := buildReconciler(cl, cfg)

	result, err := reconcile(r, "worker-1")
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter,
		"no pods → should requeue")
}

func TestPodNotReady_PodPending(t *testing.T) {
	cfg := testConfig()
	proxy := dsKubeProxy()
	exporter := dsNodeExporter()
	cni := dsAWSCNI()

	cl := buildClient(clusterObjects(
		readyPod(proxy, "worker-1"),
		pendingPod(exporter, "worker-1"), // still scheduling
		readyPod(cni, "worker-1"),
	)...)
	r := buildReconciler(cl, cfg)

	result, err := reconcile(r, "worker-1")
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter,
		"node-exporter Pending → should requeue")
}

func TestPodNotReady_PodCrashing(t *testing.T) {
	cfg := testConfig()
	proxy := dsKubeProxy()
	exporter := dsNodeExporter()
	cni := dsAWSCNI()

	// node-exporter is Running but NOT Ready (e.g. CrashLoopBackOff) on worker-1.
	// Also test crashing on worker-2 to ensure node independence.
	cl := buildClient(clusterObjects(
		readyPod(proxy, "worker-1"),
		crashingPod(exporter, "worker-1"),
		readyPod(cni, "worker-1"),
		readyPod(proxy, "worker-2"),
		crashingPod(exporter, "worker-2"),
		readyPod(cni, "worker-2"),
	)...)
	r := buildReconciler(cl, cfg)

	result, err := reconcile(r, "worker-1")
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter,
		"worker-1: node-exporter Running but not Ready → should requeue")

	result, err = reconcile(r, "worker-2")
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter,
		"worker-2: node-exporter Running but not Ready → should requeue")
}

func TestPodNotReady_PodFailed(t *testing.T) {
	cfg := testConfig()
	proxy := dsKubeProxy()
	exporter := dsNodeExporter()
	cni := dsAWSCNI()

	// node-exporter Failed on worker-1, aws-cni Failed on arm-1.
	arm := dsARMMonitor()
	cl := buildClient(clusterObjects(
		readyPod(proxy, "worker-1"),
		failedPod(exporter, "worker-1"),
		readyPod(cni, "worker-1"),
		readyPod(proxy, "arm-1"),
		readyPod(exporter, "arm-1"),
		failedPod(cni, "arm-1"),
		readyPod(arm, "arm-1"),
	)...)
	r := buildReconciler(cl, cfg)

	result, err := reconcile(r, "worker-1")
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter,
		"worker-1: node-exporter Failed → should requeue")

	result, err = reconcile(r, "arm-1")
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter,
		"arm-1: aws-cni Failed → should requeue")
}

func TestPodNotReady_OrphanPodIgnored(t *testing.T) {
	cfg := testConfig()
	proxy := dsKubeProxy()
	cni := dsAWSCNI()

	// Orphan pod on worker-1 — Running+Ready but no DaemonSet owner.
	orphan := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "kube-system",
			Name:      "orphan-pod",
		},
		Spec: corev1.PodSpec{
			NodeName:   "worker-1",
			Containers: []corev1.Container{{Name: "main", Image: "example:latest"}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}

	// Only 2 of 3 expected DS have pods. Orphan shouldn't fill the gap.
	cl := buildClient(append(clusterObjects(
		readyPod(proxy, "worker-1"),
		readyPod(cni, "worker-1"),
	), orphan)...)
	r := buildReconciler(cl, cfg)

	result, err := reconcile(r, "worker-1")
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter,
		"orphan pod should not count — node-exporter still missing")
}

// ===================================================================
// Test: Multiple nodes, independent readiness
// ===================================================================

func TestMultipleNodes_IndependentReadiness(t *testing.T) {
	cfg := testConfig()
	proxy := dsKubeProxy()
	exporter := dsNodeExporter()
	cni := dsAWSCNI()
	gpu := dsGPUDriver()

	cl := buildClient(clusterObjects(
		// worker-1: all 3 Ready.
		readyPod(proxy, "worker-1"),
		readyPod(exporter, "worker-1"),
		readyPod(cni, "worker-1"),
		// worker-2: 1 of 3 Ready.
		readyPod(proxy, "worker-2"),
		// gpu-1: 2 of 3 Ready.
		readyPod(proxy, "gpu-1"),
		readyPod(exporter, "gpu-1"),
		// arm-1: no pods at all.
	)...)
	r := buildReconciler(cl, cfg)

	// worker-1: ready.
	result, err := reconcile(r, "worker-1")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result, "worker-1 should be ready (3/3)")

	// worker-2: not ready.
	result, err = reconcile(r, "worker-2")
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter, "worker-2 should requeue (1/3)")

	// gpu-1: not ready (missing gpu-driver).
	result, err = reconcile(r, "gpu-1")
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter, "gpu-1 should requeue (2/3)")

	// arm-1: not ready (0/4).
	result, err = reconcile(r, "arm-1")
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter, "arm-1 should requeue (0/4)")

	// stable-1: no taint, skipped.
	result, err = reconcile(r, "stable-1")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result, "stable-1 skipped (no taint)")

	// Now add the missing pods and re-reconcile.

	// gpu-1: add gpu-driver pod.
	require.NoError(t, cl.Create(context.Background(), readyPod(gpu, "gpu-1")))
	result, err = reconcile(r, "gpu-1")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result, "gpu-1 should now be ready (3/3)")

	// worker-2: add missing pods.
	require.NoError(t, cl.Create(context.Background(), readyPod(exporter, "worker-2")))
	require.NoError(t, cl.Create(context.Background(), readyPod(cni, "worker-2")))
	result, err = reconcile(r, "worker-2")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result, "worker-2 should now be ready (3/3)")
}

// ===================================================================
// Test: Node lifecycle — pods come up gradually
// ===================================================================

func TestLifecycle_PodsAppearGradually(t *testing.T) {
	cfg := testConfig()
	proxy := dsKubeProxy()
	exporter := dsNodeExporter()
	cni := dsAWSCNI()
	arm := dsARMMonitor()

	// arm-1 starts with zero pods.
	cl := buildClient(clusterObjects()...)
	r := buildReconciler(cl, cfg)

	// Round 1: 0/4 Ready → requeue.
	result, err := reconcile(r, "arm-1")
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter, "round 1: 0/4")

	// Round 2: kube-proxy appears but Pending.
	require.NoError(t, cl.Create(context.Background(), pendingPod(proxy, "arm-1")))
	result, err = reconcile(r, "arm-1")
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter, "round 2: 0/4 (1 Pending)")

	// Round 3: kube-proxy becomes Ready, node-exporter appears Ready.
	var p corev1.Pod
	require.NoError(t, cl.Get(context.Background(),
		types.NamespacedName{Namespace: proxy.Namespace, Name: proxy.Name + "-arm-1"}, &p))
	p.Status.Phase = corev1.PodRunning
	p.Status.Conditions = []corev1.PodCondition{
		{Type: corev1.PodReady, Status: corev1.ConditionTrue},
	}
	require.NoError(t, cl.Status().Update(context.Background(), &p))
	require.NoError(t, cl.Create(context.Background(), readyPod(exporter, "arm-1")))
	result, err = reconcile(r, "arm-1")
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter, "round 3: 2/4")

	// Round 4: aws-cni and arm-monitor appear Ready.
	require.NoError(t, cl.Create(context.Background(), readyPod(cni, "arm-1")))
	require.NoError(t, cl.Create(context.Background(), readyPod(arm, "arm-1")))
	result, err = reconcile(r, "arm-1")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result, "round 4: 4/4 — all Ready")
}

// ===================================================================
// Test: Pod regresses from Ready to not Ready
// ===================================================================

func TestLifecycle_PodRegresses(t *testing.T) {
	cfg := testConfig()
	proxy := dsKubeProxy()
	exporter := dsNodeExporter()
	cni := dsAWSCNI()

	// Start with all Ready on worker-2.
	cl := buildClient(clusterObjects(
		readyPod(proxy, "worker-2"),
		readyPod(exporter, "worker-2"),
		readyPod(cni, "worker-2"),
	)...)
	r := buildReconciler(cl, cfg)

	// First reconcile: all Ready → taint removed.
	result, err := reconcile(r, "worker-2")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result, "initially all Ready — taint removed")

	// Verify taint was actually removed.
	var node corev1.Node
	require.NoError(t, cl.Get(context.Background(),
		types.NamespacedName{Name: "worker-2"}, &node))
	for _, taint := range node.Spec.Taints {
		assert.NotEqual(t, cfg.TaintKey, taint.Key,
			"startup taint should have been removed")
	}

	// aws-cni crashes — Ready condition goes False.
	var p corev1.Pod
	require.NoError(t, cl.Get(context.Background(),
		types.NamespacedName{Namespace: cni.Namespace, Name: cni.Name + "-worker-2"}, &p))
	p.Status.Conditions = []corev1.PodCondition{
		{Type: corev1.PodReady, Status: corev1.ConditionFalse},
	}
	require.NoError(t, cl.Status().Update(context.Background(), &p))

	// Second reconcile: taint already gone, controller skips this node.
	result, err = reconcile(r, "worker-2")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result,
		"taint already removed — controller is done with this node")
}

// ===================================================================
// Test: Node not found (deleted between list and reconcile)
// ===================================================================

func TestNodeNotFound(t *testing.T) {
	cfg := testConfig()
	cl := buildClient(clusterObjects()...)
	r := buildReconciler(cl, cfg)

	result, err := reconcile(r, "deleted-node")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result, "deleted node should not error")
}

// ===================================================================
// Test: No DaemonSets in cluster
// ===================================================================

func TestNoDaemonSets(t *testing.T) {
	cfg := testConfig()
	node := nodeWorker("lonely-node")
	cl := buildClient(node)
	r := buildReconciler(cl, cfg)

	// 0 expected → 0 ready → all ready (vacuously true) → taint removed.
	result, err := reconcile(r, "lonely-node")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result,
		"no DaemonSets → vacuously ready, taint removed")

	// Verify taint was removed.
	var updated corev1.Node
	require.NoError(t, cl.Get(context.Background(),
		types.NamespacedName{Name: "lonely-node"}, &updated))
	assert.Empty(t, updated.Spec.Taints, "taint should be removed")
}

// ===================================================================
// Test: Taint removal verifies taint is gone from node
// ===================================================================

func TestTaintRemoval_VerifyTaintGone(t *testing.T) {
	cfg := testConfig()
	proxy := dsKubeProxy()
	exporter := dsNodeExporter()
	cni := dsAWSCNI()

	cl := buildClient(clusterObjects(
		readyPod(proxy, "worker-1"),
		readyPod(exporter, "worker-1"),
		readyPod(cni, "worker-1"),
	)...)
	r := buildReconciler(cl, cfg)

	result, err := reconcile(r, "worker-1")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Verify taint was removed from the node.
	var node corev1.Node
	require.NoError(t, cl.Get(context.Background(),
		types.NamespacedName{Name: "worker-1"}, &node))
	for _, taint := range node.Spec.Taints {
		assert.NotEqual(t, cfg.TaintKey, taint.Key,
			"startup taint should have been removed from worker-1")
	}
}

// ===================================================================
// Test: Timeout removes taint even when pods are not ready
// ===================================================================

func TestTimeout_RemovesTaintWhenNotReady(t *testing.T) {
	cfg := testConfig()
	cfg.TimeoutSeconds = 1 // 1 second timeout

	proxy := dsKubeProxy()

	// Create a node with a creation timestamp in the past (older than timeout).
	node := nodeWorker("old-worker")
	node.CreationTimestamp = metav1.NewTime(time.Now().Add(-2 * time.Minute))

	cl := buildClient(append(clusterObjects(
		// Only kube-proxy has a pod, but it's Pending.
		pendingPod(proxy, "old-worker"),
	), node)...)
	r := buildReconciler(cl, cfg)

	result, err := reconcile(r, "old-worker")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result,
		"timeout should remove taint even with not-ready pods")

	// Verify taint was removed.
	var updated corev1.Node
	require.NoError(t, cl.Get(context.Background(),
		types.NamespacedName{Name: "old-worker"}, &updated))
	for _, taint := range updated.Spec.Taints {
		assert.NotEqual(t, cfg.TaintKey, taint.Key,
			"startup taint should be removed after timeout")
	}
}

func TestTimeout_DoesNotFireForYoungNode(t *testing.T) {
	cfg := testConfig()
	cfg.TimeoutSeconds = 120

	// Node was just created — should NOT timeout.
	cl := buildClient(clusterObjects()...)
	r := buildReconciler(cl, cfg)

	result, err := reconcile(r, "worker-1")
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter,
		"young node should requeue, not timeout")
}

// ===================================================================
// Test: Dry-run mode logs but does not remove taint
// ===================================================================

func TestDryRun_DoesNotRemoveTaint(t *testing.T) {
	cfg := testConfig()
	cfg.DryRun = true

	proxy := dsKubeProxy()
	exporter := dsNodeExporter()
	cni := dsAWSCNI()

	cl := buildClient(clusterObjects(
		readyPod(proxy, "worker-1"),
		readyPod(exporter, "worker-1"),
		readyPod(cni, "worker-1"),
	)...)
	r := buildReconciler(cl, cfg)

	result, err := reconcile(r, "worker-1")
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter,
		"dry-run should requeue (taint not actually removed)")

	// Verify taint is still present.
	var node corev1.Node
	require.NoError(t, cl.Get(context.Background(),
		types.NamespacedName{Name: "worker-1"}, &node))
	hasTaint := false
	for _, taint := range node.Spec.Taints {
		if taint.Key == cfg.TaintKey {
			hasTaint = true
		}
	}
	assert.True(t, hasTaint, "dry-run should NOT remove the taint")
}

func TestDryRun_TimeoutAlsoDoesNotRemoveTaint(t *testing.T) {
	cfg := testConfig()
	cfg.DryRun = true
	cfg.TimeoutSeconds = 1

	node := nodeWorker("old-dry-run")
	node.CreationTimestamp = metav1.NewTime(time.Now().Add(-2 * time.Minute))

	cl := buildClient(append(clusterObjects(), node)...)
	r := buildReconciler(cl, cfg)

	result, err := reconcile(r, "old-dry-run")
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter,
		"dry-run timeout should requeue")

	// Verify taint is still present.
	var updated corev1.Node
	require.NoError(t, cl.Get(context.Background(),
		types.NamespacedName{Name: "old-dry-run"}, &updated))
	hasTaintKey := false
	for _, taint := range updated.Spec.Taints {
		if taint.Key == cfg.TaintKey {
			hasTaintKey = true
		}
	}
	assert.True(t, hasTaintKey, "dry-run timeout should NOT remove the taint")
}

// ===================================================================
// Test: Taint removal preserves other taints
// ===================================================================

func TestTaintRemoval_PreservesOtherTaints(t *testing.T) {
	cfg := testConfig()
	proxy := dsKubeProxy()
	exporter := dsNodeExporter()
	gpu := dsGPUDriver()

	// gpu-1 has both our startup taint AND dedicated=gpu taint.
	cl := buildClient(clusterObjects(
		readyPod(proxy, "gpu-1"),
		readyPod(exporter, "gpu-1"),
		readyPod(gpu, "gpu-1"),
	)...)
	r := buildReconciler(cl, cfg)

	result, err := reconcile(r, "gpu-1")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	var node corev1.Node
	require.NoError(t, cl.Get(context.Background(),
		types.NamespacedName{Name: "gpu-1"}, &node))

	// dedicated=gpu taint should still be present.
	assert.Len(t, node.Spec.Taints, 1, "should have exactly 1 taint remaining")
	assert.Equal(t, "dedicated", node.Spec.Taints[0].Key,
		"dedicated=gpu taint should be preserved")
}
