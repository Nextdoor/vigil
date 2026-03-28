package controller

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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/nextdoor/vigil/internal/discovery"
	"github.com/nextdoor/vigil/internal/readiness"
	"github.com/nextdoor/vigil/internal/taintremoval"
	"github.com/nextdoor/vigil/pkg/config"
)

func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	return s
}

func newReconciler(cl client.Client, scheme *runtime.Scheme, cfg *config.Config) *NodeReadinessReconciler {
	return &NodeReadinessReconciler{
		Client:       cl,
		Scheme:       scheme,
		Log:          logr.Discard(),
		Config:       cfg,
		Discovery:    discovery.New(cl, logr.Discard(), cfg),
		Readiness:    readiness.New(cl, logr.Discard()),
		TaintRemover: taintremoval.New(cl, cl, logr.Discard()),
	}
}

func TestReconcile_NodeWithoutTaint(t *testing.T) {
	scheme := newTestScheme()
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node",
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(node).
		WithIndex(&corev1.Pod{}, readiness.NodeNameField, func(o client.Object) []string {
			return []string{o.(*corev1.Pod).Spec.NodeName}
		}).
		Build()

	r := newReconciler(cl, scheme, config.NewDefault())

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-node"},
	})
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

func TestReconcile_NodeWithTaint_NoDaemonSets(t *testing.T) {
	scheme := newTestScheme()
	cfg := config.NewDefault()
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-node",
			CreationTimestamp: metav1.Now(),
		},
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{
				{Key: cfg.TaintKey, Effect: corev1.TaintEffectNoSchedule},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(node).
		WithIndex(&corev1.Pod{}, readiness.NodeNameField, func(o client.Object) []string {
			return []string{o.(*corev1.Pod).Spec.NodeName}
		}).
		Build()

	r := newReconciler(cl, scheme, cfg)

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-node"},
	})
	require.NoError(t, err)
	// No expected DaemonSets means all ready (0 == 0).
	assert.Equal(t, ctrl.Result{}, result)
}

func TestReconcile_NodeWithTaint_AllReady(t *testing.T) {
	scheme := newTestScheme()
	cfg := config.NewDefault()

	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "kube-system",
			Name:      "kube-proxy",
			UID:       "ds-uid-1",
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "kube-proxy"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "kube-proxy"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "kube-proxy", Image: "kube-proxy:latest"}},
				},
			},
		},
	}

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-node",
			CreationTimestamp: metav1.Now(),
		},
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{
				{Key: cfg.TaintKey, Effect: corev1.TaintEffectNoSchedule},
			},
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "kube-system",
			Name:      "kube-proxy-abc",
			OwnerReferences: []metav1.OwnerReference{
				{APIVersion: "apps/v1", Kind: "DaemonSet", Name: "kube-proxy", UID: "ds-uid-1"},
			},
		},
		Spec: corev1.PodSpec{
			NodeName:   "test-node",
			Containers: []corev1.Container{{Name: "kube-proxy", Image: "kube-proxy:latest"}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(ds, node, pod).
		WithIndex(&corev1.Pod{}, readiness.NodeNameField, func(o client.Object) []string {
			return []string{o.(*corev1.Pod).Spec.NodeName}
		}).
		Build()

	r := newReconciler(cl, scheme, cfg)

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-node"},
	})
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

func TestReconcile_NodeWithTaint_NotReady_Requeues(t *testing.T) {
	scheme := newTestScheme()
	cfg := config.NewDefault()

	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "kube-system",
			Name:      "kube-proxy",
			UID:       "ds-uid-1",
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "kube-proxy"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "kube-proxy"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "kube-proxy", Image: "kube-proxy:latest"}},
				},
			},
		},
	}

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-node",
			CreationTimestamp: metav1.Now(),
		},
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{
				{Key: cfg.TaintKey, Effect: corev1.TaintEffectNoSchedule},
			},
		},
	}

	// Pod exists but is Pending (not ready).
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "kube-system",
			Name:      "kube-proxy-abc",
			OwnerReferences: []metav1.OwnerReference{
				{APIVersion: "apps/v1", Kind: "DaemonSet", Name: "kube-proxy", UID: "ds-uid-1"},
			},
		},
		Spec: corev1.PodSpec{
			NodeName:   "test-node",
			Containers: []corev1.Container{{Name: "kube-proxy", Image: "kube-proxy:latest"}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(ds, node, pod).
		WithIndex(&corev1.Pod{}, readiness.NodeNameField, func(o client.Object) []string {
			return []string{o.(*corev1.Pod).Spec.NodeName}
		}).
		Build()

	r := newReconciler(cl, scheme, cfg)

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-node"},
	})
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter)
}

func TestReconcile_NodeNotFound(t *testing.T) {
	scheme := newTestScheme()
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithIndex(&corev1.Pod{}, readiness.NodeNameField, func(o client.Object) []string {
			return []string{o.(*corev1.Pod).Spec.NodeName}
		}).
		Build()

	r := newReconciler(cl, scheme, config.NewDefault())

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nonexistent"},
	})
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}
