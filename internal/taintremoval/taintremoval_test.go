package taintremoval

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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	return s
}

func taintedNode(name, taintKey string, extraTaints ...corev1.Taint) *corev1.Node {
	taints := make([]corev1.Taint, 0, 1+len(extraTaints))
	taints = append(taints, corev1.Taint{Key: taintKey, Effect: corev1.TaintEffectNoSchedule})
	taints = append(taints, extraTaints...)
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1.NodeSpec{Taints: taints},
	}
}

func TestRemoveTaint_Success(t *testing.T) {
	node := taintedNode("node-1", "test/initializing")
	cl := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(node).Build()

	remover := New(cl, cl, logr.Discard())
	removed, err := remover.RemoveTaint(context.Background(), "node-1", "test/initializing")
	require.NoError(t, err)
	assert.True(t, removed)

	// Verify taint is gone.
	var updated corev1.Node
	require.NoError(t, cl.Get(context.Background(), types.NamespacedName{Name: "node-1"}, &updated))
	for _, taint := range updated.Spec.Taints {
		assert.NotEqual(t, "test/initializing", taint.Key, "taint should be removed")
	}
}

func TestRemoveTaint_PreservesOtherTaints(t *testing.T) {
	node := taintedNode("node-2", "test/initializing",
		corev1.Taint{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule},
		corev1.Taint{Key: "other/taint", Effect: corev1.TaintEffectNoExecute},
	)
	cl := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(node).Build()

	remover := New(cl, cl, logr.Discard())
	removed, err := remover.RemoveTaint(context.Background(), "node-2", "test/initializing")
	require.NoError(t, err)
	assert.True(t, removed)

	var updated corev1.Node
	require.NoError(t, cl.Get(context.Background(), types.NamespacedName{Name: "node-2"}, &updated))
	assert.Len(t, updated.Spec.Taints, 2, "should preserve other taints")
	assert.Equal(t, "dedicated", updated.Spec.Taints[0].Key)
	assert.Equal(t, "other/taint", updated.Spec.Taints[1].Key)
}

func TestRemoveTaint_AlreadyAbsent(t *testing.T) {
	node := taintedNode("node-3", "other/taint")
	cl := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(node).Build()

	remover := New(cl, cl, logr.Discard())
	removed, err := remover.RemoveTaint(context.Background(), "node-3", "missing/key")
	require.NoError(t, err)
	assert.False(t, removed, "should return false when taint not present")
}

func TestRemoveTaint_NodeNotFound(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(newScheme()).Build()

	remover := New(cl, cl, logr.Discard())
	_, err := remover.RemoveTaint(context.Background(), "nonexistent", "test/initializing")
	assert.Error(t, err)
}

func TestRemoveTaintByKey(t *testing.T) {
	taints := []corev1.Taint{
		{Key: "a", Effect: corev1.TaintEffectNoSchedule},
		{Key: "b", Effect: corev1.TaintEffectNoSchedule},
		{Key: "c", Effect: corev1.TaintEffectNoExecute},
	}

	filtered, found := removeTaintByKey(taints, "b")
	assert.True(t, found)
	assert.Len(t, filtered, 2)
	assert.Equal(t, "a", filtered[0].Key)
	assert.Equal(t, "c", filtered[1].Key)

	filtered, found = removeTaintByKey(taints, "missing")
	assert.False(t, found)
	assert.Len(t, filtered, 3)
}

func TestRemoveTaintByKey_EmptyList(t *testing.T) {
	filtered, found := removeTaintByKey(nil, "anything")
	assert.False(t, found)
	assert.Empty(t, filtered)
}
