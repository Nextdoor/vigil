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

package inventory

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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	return s
}

func newDaemonSet(namespace, name string) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
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

func TestInventory_AddDaemonSet(t *testing.T) {
	scheme := newScheme()
	ds := newDaemonSet("kube-system", "kube-proxy")

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ds).Build()
	inv := New(cl, logr.Discard())

	_, err := inv.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "kube-system", Name: "kube-proxy"},
	})
	require.NoError(t, err)

	known := inv.KnownDaemonSets()
	assert.Equal(t, []string{"kube-system/kube-proxy"}, known)
}

func TestInventory_RemoveDaemonSet(t *testing.T) {
	scheme := newScheme()
	ds := newDaemonSet("kube-system", "kube-proxy")

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ds).Build()
	inv := New(cl, logr.Discard())

	// First reconcile — add.
	_, err := inv.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "kube-system", Name: "kube-proxy"},
	})
	require.NoError(t, err)
	assert.Len(t, inv.KnownDaemonSets(), 1)

	// Delete the DS from the fake client.
	require.NoError(t, cl.Delete(context.Background(), ds))

	// Second reconcile — remove.
	_, err = inv.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "kube-system", Name: "kube-proxy"},
	})
	require.NoError(t, err)
	assert.Empty(t, inv.KnownDaemonSets())
}

func TestInventory_IdempotentAdd(t *testing.T) {
	scheme := newScheme()
	ds := newDaemonSet("kube-system", "kube-proxy")

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ds).Build()
	inv := New(cl, logr.Discard())

	// Reconcile twice — should still have one entry.
	for range 2 {
		_, err := inv.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Namespace: "kube-system", Name: "kube-proxy"},
		})
		require.NoError(t, err)
	}

	assert.Equal(t, []string{"kube-system/kube-proxy"}, inv.KnownDaemonSets())
}

func TestInventory_MultipleDaemonSets(t *testing.T) {
	scheme := newScheme()
	ds1 := newDaemonSet("kube-system", "kube-proxy")
	ds2 := newDaemonSet("monitoring", "node-exporter")
	ds3 := newDaemonSet("kube-system", "aws-node")

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ds1, ds2, ds3).Build()
	inv := New(cl, logr.Discard())

	for _, ns := range []types.NamespacedName{
		{Namespace: "kube-system", Name: "kube-proxy"},
		{Namespace: "monitoring", Name: "node-exporter"},
		{Namespace: "kube-system", Name: "aws-node"},
	} {
		_, err := inv.Reconcile(context.Background(), ctrl.Request{NamespacedName: ns})
		require.NoError(t, err)
	}

	known := inv.KnownDaemonSets()
	assert.Equal(t, []string{
		"kube-system/aws-node",
		"kube-system/kube-proxy",
		"monitoring/node-exporter",
	}, known)
}

func TestInventory_RemoveUnknownIsNoop(t *testing.T) {
	scheme := newScheme()
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()
	inv := New(cl, logr.Discard())

	// Reconcile a DS that doesn't exist and isn't known — no panic, no error.
	_, err := inv.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "kube-system", Name: "gone"},
	})
	require.NoError(t, err)
	assert.Empty(t, inv.KnownDaemonSets())
}