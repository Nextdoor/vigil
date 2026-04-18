//go:build stress

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

package stress

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Pods unrelated to DaemonSets sit in the informer cache even though the
// controller never reconciles against them. Real clusters have tens of
// thousands of them, each padded with ManagedFields, annotations, env, and
// container statuses — the cache bloat from this is what dominates controller
// memory in production. Stress runs need to include them or the memory number
// is meaningless.
func createBackgroundPods(
	ctx context.Context,
	cl client.Client,
	count int,
	nodeCount int,
	minBytes, maxBytes int,
	concurrency int,
) error {
	if count <= 0 {
		return nil
	}

	rng := rand.New(rand.NewSource(1337)) //nolint:gosec // deterministic for reproducibility

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var firstErrMu sync.Mutex
	var firstErr error

	for i := range count {
		size := minBytes + rng.Intn(maxBytes-minBytes+1)
		nodeIdx := i % nodeCount
		pod := buildBackgroundPod(i, nodeIdx, size, rng)

		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		wg.Add(1)
		go func(p *corev1.Pod) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := cl.Create(ctx, p); err != nil {
				firstErrMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				firstErrMu.Unlock()
			}
		}(pod)
	}

	wg.Wait()
	return firstErr
}

// buildBackgroundPod returns a ReplicaSet-owned pod whose serialized size
// lands near targetBytes. The padding comes from a single large annotation —
// conceptually the same as a real pod's last-applied-configuration blob.
func buildBackgroundPod(idx, nodeIdx, targetBytes int, rng *rand.Rand) *corev1.Pod {
	const baseOverhead = 800 // rough non-filler object size
	fillerLen := targetBytes - baseOverhead
	if fillerLen < 0 {
		fillerLen = 0
	}
	filler := randomASCII(rng, fillerLen)

	rsIdx := idx % 100
	rsName := fmt.Sprintf("bg-rs-%03d", rsIdx)
	rsUID := types.UID(fmt.Sprintf("00000000-0000-0000-0000-%012d", rsIdx))
	ctrl := true

	imageID := "docker.io/library/example@sha256:" + randomHex(rng, 64)
	containerID := "containerd://" + randomHex(rng, 64)

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      fmt.Sprintf("bg-pod-%06d", idx),
			Labels: map[string]string{
				"app":                          rsName,
				"tier":                         "backend",
				"pod-template-hash":            fmt.Sprintf("%d", rsIdx),
				"app.kubernetes.io/managed-by": "stress-test",
			},
			Annotations: map[string]string{
				"kubectl.kubernetes.io/last-applied-configuration": filler,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "ReplicaSet",
					Name:       rsName,
					UID:        rsUID,
					Controller: &ctrl,
				},
			},
		},
		Spec: corev1.PodSpec{
			NodeName: fmt.Sprintf("stress-node-%05d", nodeIdx),
			Containers: []corev1.Container{
				{
					Name:  "app",
					Image: "example/app:v1.2.3",
					Env: []corev1.EnvVar{
						{Name: "FOO", Value: "bar"},
						{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{
							FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
						}},
					},
				},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         "app",
					Ready:        true,
					RestartCount: 0,
					Image:        "example/app:v1.2.3",
					ImageID:      imageID,
					ContainerID:  containerID,
					Started:      boolPtr(true),
					State: corev1.ContainerState{
						Running: &corev1.ContainerStateRunning{
							StartedAt: metav1.Time{Time: time.Now().Add(-time.Hour)},
						},
					},
				},
			},
		},
	}
}

func randomASCII(rng *rand.Rand, n int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 "
	b := make([]byte, n)
	for i := range b {
		b[i] = charset[rng.Intn(len(charset))]
	}
	return string(b)
}

func randomHex(rng *rand.Rand, n int) string {
	const hex = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = hex[rng.Intn(16)]
	}
	return string(b)
}

func boolPtr(b bool) *bool { return &b }
