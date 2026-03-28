package controller

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/nextdoor/vigil/pkg/config"
)

func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	return s
}

func TestReconcile_NodeWithoutTaint(t *testing.T) {
	scheme := newTestScheme()
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node",
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node).Build()
	r := &NodeReadinessReconciler{
		Client: cl,
		Scheme: scheme,
		Log:    logr.Discard(),
		Config: config.NewDefault(),
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-node"},
	})
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

func TestReconcile_NodeWithTaint(t *testing.T) {
	scheme := newTestScheme()
	cfg := config.NewDefault()
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node",
		},
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{
				{
					Key:    cfg.TaintKey,
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node).Build()
	r := &NodeReadinessReconciler{
		Client: cl,
		Scheme: scheme,
		Log:    logr.Discard(),
		Config: cfg,
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-node"},
	})
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

func TestReconcile_NodeNotFound(t *testing.T) {
	scheme := newTestScheme()
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &NodeReadinessReconciler{
		Client: cl,
		Scheme: scheme,
		Log:    logr.Discard(),
		Config: config.NewDefault(),
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nonexistent"},
	})
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}
