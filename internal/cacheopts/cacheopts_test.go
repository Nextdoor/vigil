// Copyright 2026 Nextdoor, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cacheopts

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	toolscache "k8s.io/client-go/tools/cache"
)

func TestTransformPod_PreservesFieldsVigilReads(t *testing.T) {
	controller := true
	in := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "ds-pod",
			Namespace:       "kube-system",
			UID:             "pod-uid",
			ResourceVersion: "42",
			Labels:          map[string]string{"app": "foo"},
			OwnerReferences: []metav1.OwnerReference{
				{APIVersion: "apps/v1", Kind: "DaemonSet", Name: "foo", UID: "ds-uid", Controller: &controller},
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "node-1",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodInitialized, Status: corev1.ConditionTrue},
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}

	out, err := TransformPod(in)
	require.NoError(t, err)
	pod, ok := out.(*corev1.Pod)
	require.True(t, ok)

	assert.Equal(t, "ds-pod", pod.Name)
	assert.Equal(t, "kube-system", pod.Namespace)
	assert.Equal(t, "pod-uid", string(pod.UID))
	assert.Equal(t, "42", pod.ResourceVersion)
	assert.Equal(t, "foo", pod.Labels["app"])
	assert.Equal(t, "node-1", pod.Spec.NodeName)
	assert.Equal(t, corev1.PodRunning, pod.Status.Phase)
	require.Len(t, pod.OwnerReferences, 1)
	assert.Equal(t, "DaemonSet", pod.OwnerReferences[0].Kind)
	require.Len(t, pod.Status.Conditions, 1)
	assert.Equal(t, corev1.PodReady, pod.Status.Conditions[0].Type)
	assert.Equal(t, corev1.ConditionTrue, pod.Status.Conditions[0].Status)
}

func TestTransformPod_StripsFatFields(t *testing.T) {
	in := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "fat-pod",
			ManagedFields: []metav1.ManagedFieldsEntry{
				{Manager: "kubectl", Operation: metav1.ManagedFieldsOperationUpdate},
			},
			Annotations: map[string]string{
				"kubectl.kubernetes.io/last-applied-configuration": "really long blob here",
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "n",
			Containers: []corev1.Container{
				{Name: "c", Image: "x", Env: []corev1.EnvVar{{Name: "E", Value: "v"}}},
			},
			Volumes: []corev1.Volume{{Name: "v"}},
		},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			HostIP:            "10.0.0.1",
			PodIP:             "10.0.0.2",
			ContainerStatuses: []corev1.ContainerStatus{{Name: "c", Image: "x", ImageID: "sha256:deadbeef"}},
		},
	}

	out, err := TransformPod(in)
	require.NoError(t, err)
	pod := out.(*corev1.Pod)

	assert.Nil(t, pod.ManagedFields)
	assert.Nil(t, pod.Annotations)
	assert.Empty(t, pod.Spec.Containers)
	assert.Empty(t, pod.Spec.Volumes)
	assert.Empty(t, pod.Status.ContainerStatuses)
	assert.Empty(t, pod.Status.HostIP)
	assert.Empty(t, pod.Status.PodIP)
}

func TestTransformPod_KeepsOnlyPodReadyCondition(t *testing.T) {
	in := &corev1.Pod{
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodInitialized, Status: corev1.ConditionTrue},
				{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
				{Type: corev1.ContainersReady, Status: corev1.ConditionTrue},
				{Type: corev1.PodReady, Status: corev1.ConditionFalse},
			},
		},
	}
	out, _ := TransformPod(in)
	pod := out.(*corev1.Pod)
	require.Len(t, pod.Status.Conditions, 1)
	assert.Equal(t, corev1.PodReady, pod.Status.Conditions[0].Type)
	assert.Equal(t, corev1.ConditionFalse, pod.Status.Conditions[0].Status)
}

func TestTransformPod_NoPodReadyCondition(t *testing.T) {
	in := &corev1.Pod{
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodInitialized, Status: corev1.ConditionTrue},
			},
		},
	}
	out, _ := TransformPod(in)
	pod := out.(*corev1.Pod)
	assert.Nil(t, pod.Status.Conditions)
}

func TestTransformPod_PassesThroughTombstone(t *testing.T) {
	tombstone := toolscache.DeletedFinalStateUnknown{Key: "default/pod-1", Obj: &corev1.Pod{}}
	out, err := TransformPod(tombstone)
	require.NoError(t, err)
	assert.Equal(t, tombstone, out)
}

func TestTransformPod_PassesThroughNonPod(t *testing.T) {
	in := "not-a-pod"
	out, err := TransformPod(in)
	require.NoError(t, err)
	assert.Equal(t, in, out)
}

func TestNew_RegistersPodTransform(t *testing.T) {
	opts := New()
	require.NotNil(t, opts.DefaultTransform)

	var found bool
	for k, v := range opts.ByObject {
		if _, ok := k.(*corev1.Pod); ok {
			require.NotNil(t, v.Transform)
			found = true
		}
	}
	require.True(t, found, "expected a Pod entry in ByObject")
}
