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

// Package cacheopts builds the controller-runtime cache configuration used by
// both the production binary and the stress harness. The shared definition
// keeps the stress-test memory numbers apples-to-apples with what ships.
package cacheopts

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TransformPod strips every pod field vigil does not read before the object
// enters the informer cache. On a large cluster (tens of thousands of pods)
// this is the difference between a several-hundred-megabyte cache and a small
// one — fat fields like ManagedFields, annotations, container statuses, env
// vars, and volume specs dominate in-cache pod size.
//
// Fields preserved — every field the controller actually reads:
//   - metadata.name, namespace, uid, resourceVersion
//   - metadata.labels (small, useful for log context)
//   - metadata.ownerReferences (checked for DaemonSet ownership)
//   - spec.nodeName (required for the .spec.nodeName field indexer)
//   - status.phase and the PodReady condition (readiness check)
//
// Everything else is cleared. Updating this list means updating the readiness
// package in lockstep.
func TransformPod(obj any) (any, error) {
	// Informer delete events may arrive as a tombstone wrapping the last
	// known object. Pass those through untouched.
	if _, ok := obj.(toolscache.DeletedFinalStateUnknown); ok {
		return obj, nil
	}
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return obj, nil
	}

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pod.Name,
			Namespace:       pod.Namespace,
			UID:             pod.UID,
			ResourceVersion: pod.ResourceVersion,
			Labels:          pod.Labels,
			OwnerReferences: pod.OwnerReferences,
		},
		Spec: corev1.PodSpec{
			NodeName: pod.Spec.NodeName,
		},
		Status: corev1.PodStatus{
			Phase:      pod.Status.Phase,
			Conditions: podReadyConditionOnly(pod.Status.Conditions),
		},
	}, nil
}

func podReadyConditionOnly(in []corev1.PodCondition) []corev1.PodCondition {
	for _, c := range in {
		if c.Type == corev1.PodReady {
			return []corev1.PodCondition{c}
		}
	}
	return nil
}

// New returns the cache.Options the manager should use. A pod Transform is
// always applied; ManagedFields is also stripped from every other type via
// the default transform.
func New() cache.Options {
	return cache.Options{
		DefaultTransform: cache.TransformStripManagedFields(),
		ByObject: map[client.Object]cache.ByObject{
			&corev1.Pod{}: {Transform: TransformPod},
		},
	}
}
