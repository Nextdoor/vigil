package controller_test

import (
	"context"
	"fmt"
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
	"github.com/nextdoor/vigil/pkg/config"
)

// Integration tests exercise the full reconciliation pipeline:
// discovery → readiness checking → requeue/ready decision.

func buildClient(objs ...client.Object) client.WithWatch {
	return fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(objs...).
		WithIndex(&corev1.Pod{}, readiness.NodeNameField, func(o client.Object) []string {
			pod := o.(*corev1.Pod)
			if pod.Spec.NodeName == "" {
				return nil
			}
			return []string{pod.Spec.NodeName}
		}).
		Build()
}

func buildReconciler(cl client.Client, cfg *config.Config) *controller.NodeReadinessReconciler {
	return &controller.NodeReadinessReconciler{
		Client:    cl,
		Scheme:    testScheme,
		Log:       logr.Discard(),
		Config:    cfg,
		Discovery: discovery.New(cl, logr.Discard(), cfg),
		Readiness: readiness.New(cl, logr.Discard()),
	}
}

func makeDaemonSet(namespace, name string) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			UID:       types.UID(fmt.Sprintf("ds-%s-%s", namespace, name)),
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
					Containers: []corev1.Container{
						{Name: name, Image: "example/" + name + ":latest"},
					},
				},
			},
		},
	}
}

func makeNode(name string, taintKey string) *corev1.Node {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			CreationTimestamp: metav1.Now(),
		},
	}
	if taintKey != "" {
		node.Spec.Taints = []corev1.Taint{
			{Key: taintKey, Effect: corev1.TaintEffectNoSchedule},
		}
	}
	return node
}

func makePod(namespace, name, nodeName string, ownerDS *appsv1.DaemonSet, phase corev1.PodPhase, ready bool) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Spec: corev1.PodSpec{
			NodeName:   nodeName,
			Containers: []corev1.Container{{Name: "main", Image: "example:latest"}},
		},
		Status: corev1.PodStatus{
			Phase: phase,
		},
	}

	if ownerDS != nil {
		pod.OwnerReferences = []metav1.OwnerReference{
			{
				APIVersion: "apps/v1",
				Kind:       "DaemonSet",
				Name:       ownerDS.Name,
				UID:        ownerDS.UID,
			},
		}
	}

	if ready {
		pod.Status.Conditions = []corev1.PodCondition{
			{Type: corev1.PodReady, Status: corev1.ConditionTrue},
		}
	}

	return pod
}

func reconcile(r *controller.NodeReadinessReconciler, nodeName string) (ctrl.Result, error) {
	return r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: nodeName},
	})
}

// TestIntegration_TaintedNode_AllDSReady verifies the full pipeline when
// a tainted node has all expected DaemonSet pods Running and Ready.
func TestIntegration_TaintedNode_AllDSReady(t *testing.T) {
	cfg := &config.Config{
		TaintKey:    "node.example.com/initializing",
		TaintEffect: "NoSchedule",
		StartupTaintKeys: []string{
			"node.example.com/initializing",
		},
		TimeoutSeconds: 120,
	}

	node := makeNode("worker-1", cfg.TaintKey)
	ds1 := makeDaemonSet("kube-system", "kube-proxy")
	ds2 := makeDaemonSet("kube-system", "node-exporter")

	pod1 := makePod("kube-system", "kube-proxy-w1", "worker-1", ds1, corev1.PodRunning, true)
	pod2 := makePod("kube-system", "node-exporter-w1", "worker-1", ds2, corev1.PodRunning, true)

	cl := buildClient(node, ds1, ds2, pod1, pod2)
	r := buildReconciler(cl, cfg)

	result, err := reconcile(r, "worker-1")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result, "should not requeue when all DS pods are Ready")
}

// TestIntegration_TaintedNode_SomeDSNotReady verifies requeue when some
// DaemonSet pods are not yet Ready.
func TestIntegration_TaintedNode_SomeDSNotReady(t *testing.T) {
	cfg := &config.Config{
		TaintKey:    "node.example.com/initializing",
		TaintEffect: "NoSchedule",
		StartupTaintKeys: []string{
			"node.example.com/initializing",
		},
		TimeoutSeconds: 120,
	}

	node := makeNode("worker-1", cfg.TaintKey)
	ds1 := makeDaemonSet("kube-system", "kube-proxy")
	ds2 := makeDaemonSet("kube-system", "cni-plugin")

	pod1 := makePod("kube-system", "kube-proxy-w1", "worker-1", ds1, corev1.PodRunning, true)
	// cni-plugin pod is Pending — not ready yet.
	pod2 := makePod("kube-system", "cni-plugin-w1", "worker-1", ds2, corev1.PodPending, false)

	cl := buildClient(node, ds1, ds2, pod1, pod2)
	r := buildReconciler(cl, cfg)

	result, err := reconcile(r, "worker-1")
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter,
		"should requeue with delay when some DS pods are not Ready")
}

// TestIntegration_TaintedNode_NoPods verifies requeue when expected
// DaemonSet pods haven't been created yet.
func TestIntegration_TaintedNode_NoPods(t *testing.T) {
	cfg := &config.Config{
		TaintKey:    "node.example.com/initializing",
		TaintEffect: "NoSchedule",
		StartupTaintKeys: []string{
			"node.example.com/initializing",
		},
		TimeoutSeconds: 120,
	}

	node := makeNode("worker-1", cfg.TaintKey)
	ds := makeDaemonSet("kube-system", "kube-proxy")
	// No pods at all — DS hasn't scheduled yet.

	cl := buildClient(node, ds)
	r := buildReconciler(cl, cfg)

	result, err := reconcile(r, "worker-1")
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter,
		"should requeue when no DS pods exist yet")
}

// TestIntegration_TaintedNode_PodBecomesReady verifies that a second
// reconcile returns ready after the pod transitions to Running+Ready.
func TestIntegration_TaintedNode_PodBecomesReady(t *testing.T) {
	cfg := &config.Config{
		TaintKey:    "node.example.com/initializing",
		TaintEffect: "NoSchedule",
		StartupTaintKeys: []string{
			"node.example.com/initializing",
		},
		TimeoutSeconds: 120,
	}

	node := makeNode("worker-1", cfg.TaintKey)
	ds := makeDaemonSet("kube-system", "kube-proxy")

	// Initially Pending.
	pod := makePod("kube-system", "kube-proxy-w1", "worker-1", ds, corev1.PodPending, false)

	cl := buildClient(node, ds, pod)
	r := buildReconciler(cl, cfg)

	// First reconcile — should requeue.
	result, err := reconcile(r, "worker-1")
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter)

	// Simulate pod becoming Ready.
	var updatedPod corev1.Pod
	require.NoError(t, cl.Get(context.Background(),
		types.NamespacedName{Namespace: "kube-system", Name: "kube-proxy-w1"}, &updatedPod))
	updatedPod.Status.Phase = corev1.PodRunning
	updatedPod.Status.Conditions = []corev1.PodCondition{
		{Type: corev1.PodReady, Status: corev1.ConditionTrue},
	}
	require.NoError(t, cl.Status().Update(context.Background(), &updatedPod))

	// Second reconcile — should succeed (no requeue).
	result, err = reconcile(r, "worker-1")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result, "should not requeue after pod becomes Ready")
}

// TestIntegration_UntaintedNode_Skipped verifies that nodes without
// the startup taint are skipped entirely.
func TestIntegration_UntaintedNode_Skipped(t *testing.T) {
	cfg := &config.Config{
		TaintKey:    "node.example.com/initializing",
		TaintEffect: "NoSchedule",
		StartupTaintKeys: []string{
			"node.example.com/initializing",
		},
		TimeoutSeconds: 120,
	}

	node := makeNode("worker-1", "") // No taint.
	ds := makeDaemonSet("kube-system", "kube-proxy")

	cl := buildClient(node, ds)
	r := buildReconciler(cl, cfg)

	result, err := reconcile(r, "worker-1")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result, "untainted node should be skipped immediately")
}

// TestIntegration_MultipleNodes verifies independent reconciliation of
// two tainted nodes with different readiness states.
func TestIntegration_MultipleNodes(t *testing.T) {
	cfg := &config.Config{
		TaintKey:    "node.example.com/initializing",
		TaintEffect: "NoSchedule",
		StartupTaintKeys: []string{
			"node.example.com/initializing",
		},
		TimeoutSeconds: 120,
	}

	node1 := makeNode("worker-1", cfg.TaintKey)
	node2 := makeNode("worker-2", cfg.TaintKey)
	ds := makeDaemonSet("kube-system", "kube-proxy")

	// worker-1 has a Ready pod, worker-2 does not.
	pod1 := makePod("kube-system", "kube-proxy-w1", "worker-1", ds, corev1.PodRunning, true)

	cl := buildClient(node1, node2, ds, pod1)
	r := buildReconciler(cl, cfg)

	// worker-1 should be ready.
	result, err := reconcile(r, "worker-1")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result, "worker-1 should be ready")

	// worker-2 should requeue (no pod).
	result, err = reconcile(r, "worker-2")
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter, "worker-2 should requeue")
}

// TestIntegration_ExcludedDaemonSet verifies that excluded DaemonSets
// are not counted in the readiness evaluation.
func TestIntegration_ExcludedDaemonSet(t *testing.T) {
	cfg := &config.Config{
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
		},
	}

	node := makeNode("worker-1", cfg.TaintKey)
	dsGood := makeDaemonSet("kube-system", "kube-proxy")
	dsSlow := makeDaemonSet("kube-system", "slow-ds")

	// kube-proxy is Ready. slow-ds has no pod — but it's excluded.
	pod := makePod("kube-system", "kube-proxy-w1", "worker-1", dsGood, corev1.PodRunning, true)

	cl := buildClient(node, dsGood, dsSlow, pod)
	r := buildReconciler(cl, cfg)

	result, err := reconcile(r, "worker-1")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result,
		"should be ready — excluded DS should not block readiness")
}

// TestIntegration_NodeSelectorFiltering verifies that DaemonSets with
// non-matching nodeSelectors are excluded from readiness evaluation.
func TestIntegration_NodeSelectorFiltering(t *testing.T) {
	cfg := &config.Config{
		TaintKey:    "node.example.com/initializing",
		TaintEffect: "NoSchedule",
		StartupTaintKeys: []string{
			"node.example.com/initializing",
		},
		TimeoutSeconds: 120,
	}

	node := makeNode("worker-1", cfg.TaintKey)
	node.Labels = map[string]string{"kubernetes.io/os": "linux"}

	// kube-proxy matches any node (no nodeSelector).
	dsProxy := makeDaemonSet("kube-system", "kube-proxy")

	// gpu-driver requires accelerator=nvidia — won't match worker-1.
	dsGPU := makeDaemonSet("kube-system", "gpu-driver")
	dsGPU.Spec.Template.Spec.NodeSelector = map[string]string{"accelerator": "nvidia"}

	// Only kube-proxy should be expected. Its pod is Ready.
	pod := makePod("kube-system", "kube-proxy-w1", "worker-1", dsProxy, corev1.PodRunning, true)

	cl := buildClient(node, dsProxy, dsGPU, pod)
	r := buildReconciler(cl, cfg)

	result, err := reconcile(r, "worker-1")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result,
		"should be ready — gpu-driver doesn't target this node")
}

// TestIntegration_StartupTaintStripping verifies that DaemonSets that can't
// tolerate a non-startup taint are correctly excluded.
func TestIntegration_StartupTaintStripping(t *testing.T) {
	cfg := &config.Config{
		TaintKey:    "node.example.com/initializing",
		TaintEffect: "NoSchedule",
		StartupTaintKeys: []string{
			"node.example.com/initializing",
		},
		TimeoutSeconds: 120,
	}

	node := makeNode("worker-1", cfg.TaintKey)
	// Node also has a dedicated=gpu taint (non-startup).
	node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{
		Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule,
	})

	// gpu-monitor tolerates the dedicated taint — should be expected.
	dsGPU := makeDaemonSet("kube-system", "gpu-monitor")
	dsGPU.Spec.Template.Spec.Tolerations = []corev1.Toleration{
		{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "gpu", Effect: corev1.TaintEffectNoSchedule},
	}

	// plain-agent has no tolerations — can't tolerate dedicated=gpu.
	dsPlain := makeDaemonSet("kube-system", "plain-agent")

	// gpu-monitor pod is Ready.
	pod := makePod("kube-system", "gpu-monitor-w1", "worker-1", dsGPU, corev1.PodRunning, true)

	cl := buildClient(node, dsGPU, dsPlain, pod)
	r := buildReconciler(cl, cfg)

	result, err := reconcile(r, "worker-1")
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result,
		"should be ready — plain-agent is filtered by toleration mismatch")
}
