package readiness

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	return s
}

func newDaemonSet(namespace, name string) appsv1.DaemonSet {
	return appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			UID:       types.UID(namespace + "-" + name),
		},
	}
}

func newPod(name, nodeName string, ownerDS *appsv1.DaemonSet, phase corev1.PodPhase, ready bool) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "kube-system",
			Name:      name,
		},
		Spec: corev1.PodSpec{
			NodeName: nodeName,
			Containers: []corev1.Container{
				{Name: "main", Image: "example:latest"},
			},
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

func TestCheckNode_AllReady(t *testing.T) {
	scheme := newScheme()
	ds1 := newDaemonSet("kube-system", "kube-proxy")
	ds2 := newDaemonSet("kube-system", "aws-node")

	pod1 := newPod("kube-proxy-abc", "node-1", &ds1, corev1.PodRunning, true)
	pod2 := newPod("aws-node-xyz", "node-1", &ds2, corev1.PodRunning, true)

	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(pod1, pod2).
		WithIndex(&corev1.Pod{}, NodeNameField, func(o client.Object) []string {
			return []string{o.(*corev1.Pod).Spec.NodeName}
		}).
		Build()

	checker := New(cl, logr.Discard())
	statuses, err := checker.CheckNode(context.Background(), "node-1", []appsv1.DaemonSet{ds1, ds2})
	require.NoError(t, err)

	assert.Len(t, statuses, 2)
	assert.True(t, statuses[0].Ready)
	assert.True(t, statuses[1].Ready)
	assert.Equal(t, 2, CountReady(statuses))
	assert.Empty(t, NotReadyNames(statuses))
}

func TestCheckNode_SomeNotReady(t *testing.T) {
	scheme := newScheme()
	ds1 := newDaemonSet("kube-system", "kube-proxy")
	ds2 := newDaemonSet("kube-system", "aws-node")

	pod1 := newPod("kube-proxy-abc", "node-1", &ds1, corev1.PodRunning, true)
	pod2 := newPod("aws-node-xyz", "node-1", &ds2, corev1.PodPending, false)

	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(pod1, pod2).
		WithIndex(&corev1.Pod{}, NodeNameField, func(o client.Object) []string {
			return []string{o.(*corev1.Pod).Spec.NodeName}
		}).
		Build()

	checker := New(cl, logr.Discard())
	statuses, err := checker.CheckNode(context.Background(), "node-1", []appsv1.DaemonSet{ds1, ds2})
	require.NoError(t, err)

	assert.Equal(t, 1, CountReady(statuses))
	assert.Equal(t, []string{"kube-system/aws-node"}, NotReadyNames(statuses))
}

func TestCheckNode_NoPodFound(t *testing.T) {
	scheme := newScheme()
	ds1 := newDaemonSet("kube-system", "kube-proxy")

	// No pods at all.
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithIndex(&corev1.Pod{}, NodeNameField, func(o client.Object) []string {
			return []string{o.(*corev1.Pod).Spec.NodeName}
		}).
		Build()

	checker := New(cl, logr.Discard())
	statuses, err := checker.CheckNode(context.Background(), "node-1", []appsv1.DaemonSet{ds1})
	require.NoError(t, err)

	assert.Len(t, statuses, 1)
	assert.False(t, statuses[0].Ready)
	assert.Empty(t, statuses[0].PodName)
}

func TestCheckNode_PodOnDifferentNode(t *testing.T) {
	scheme := newScheme()
	ds1 := newDaemonSet("kube-system", "kube-proxy")

	// Pod is on node-2, we're checking node-1.
	pod := newPod("kube-proxy-abc", "node-2", &ds1, corev1.PodRunning, true)

	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(pod).
		WithIndex(&corev1.Pod{}, NodeNameField, func(o client.Object) []string {
			return []string{o.(*corev1.Pod).Spec.NodeName}
		}).
		Build()

	checker := New(cl, logr.Discard())
	statuses, err := checker.CheckNode(context.Background(), "node-1", []appsv1.DaemonSet{ds1})
	require.NoError(t, err)

	assert.False(t, statuses[0].Ready)
}

func TestCheckNode_PodWithoutOwner(t *testing.T) {
	scheme := newScheme()
	ds1 := newDaemonSet("kube-system", "kube-proxy")

	// Pod on the right node but no owner reference.
	pod := newPod("orphan-pod", "node-1", nil, corev1.PodRunning, true)

	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(pod).
		WithIndex(&corev1.Pod{}, NodeNameField, func(o client.Object) []string {
			return []string{o.(*corev1.Pod).Spec.NodeName}
		}).
		Build()

	checker := New(cl, logr.Discard())
	statuses, err := checker.CheckNode(context.Background(), "node-1", []appsv1.DaemonSet{ds1})
	require.NoError(t, err)

	assert.False(t, statuses[0].Ready)
}

func TestIsPodReady(t *testing.T) {
	tests := []struct {
		name  string
		phase corev1.PodPhase
		ready bool
		want  bool
	}{
		{"running and ready", corev1.PodRunning, true, true},
		{"running not ready", corev1.PodRunning, false, false},
		{"pending and ready condition", corev1.PodPending, true, false},
		{"succeeded", corev1.PodSucceeded, false, false},
		{"failed", corev1.PodFailed, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := &corev1.Pod{
				Status: corev1.PodStatus{Phase: tt.phase},
			}
			if tt.ready {
				pod.Status.Conditions = []corev1.PodCondition{
					{Type: corev1.PodReady, Status: corev1.ConditionTrue},
				}
			}
			assert.Equal(t, tt.want, IsPodReady(pod))
		})
	}
}

func TestCountReady(t *testing.T) {
	statuses := []DaemonSetStatus{
		{Ready: true},
		{Ready: false},
		{Ready: true},
	}
	assert.Equal(t, 2, CountReady(statuses))
}

func TestNotReadyNames(t *testing.T) {
	statuses := []DaemonSetStatus{
		{DaemonSet: newDaemonSet("ns1", "ds1"), Ready: true},
		{DaemonSet: newDaemonSet("ns2", "ds2"), Ready: false},
		{DaemonSet: newDaemonSet("ns3", "ds3"), Ready: false},
	}
	assert.Equal(t, []string{"ns2/ds2", "ns3/ds3"}, NotReadyNames(statuses))
}
