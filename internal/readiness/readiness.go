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

package readiness

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// NodeNameField is the field index key for pod spec.nodeName.
	NodeNameField = ".spec.nodeName"
)

// PodReadinessChecker checks whether DaemonSet pods are Ready on a given node.
type PodReadinessChecker struct {
	client client.Reader
	log    logr.Logger
}

// New creates a new PodReadinessChecker.
func New(cl client.Reader, log logr.Logger) *PodReadinessChecker {
	return &PodReadinessChecker{
		client: cl,
		log:    log,
	}
}

// DaemonSetStatus holds the readiness result for a single DaemonSet on a node.
type DaemonSetStatus struct {
	DaemonSet appsv1.DaemonSet
	Ready     bool
	PodName   string // empty if no pod found
}

// CheckNode evaluates pod readiness for each expected DaemonSet on the given node.
func (c *PodReadinessChecker) CheckNode(ctx context.Context, nodeName string, expectedDS []appsv1.DaemonSet) ([]DaemonSetStatus, error) {
	// List all pods on this node using the field index.
	var podList corev1.PodList
	if err := c.client.List(ctx, &podList, client.MatchingFields{NodeNameField: nodeName}); err != nil {
		return nil, fmt.Errorf("listing pods on node %s: %w", nodeName, err)
	}

	// Build a lookup: owning DaemonSet UID -> pod.
	podByOwner := make(map[string]*corev1.Pod, len(podList.Items))
	for i := range podList.Items {
		pod := &podList.Items[i]
		for _, ref := range pod.OwnerReferences {
			if ref.Kind == "DaemonSet" {
				podByOwner[string(ref.UID)] = pod
				break
			}
		}
	}

	results := make([]DaemonSetStatus, len(expectedDS))
	for i, ds := range expectedDS {
		status := DaemonSetStatus{DaemonSet: ds}

		pod, found := podByOwner[string(ds.UID)]
		if found {
			status.PodName = pod.Name
			status.Ready = IsPodReady(pod)
		}

		results[i] = status
	}

	return results, nil
}

// IsPodReady returns true if the pod is Running and has the Ready condition set to True.
func IsPodReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

// CountReady returns the number of ready DaemonSets from a status slice.
func CountReady(statuses []DaemonSetStatus) int {
	count := 0
	for _, s := range statuses {
		if s.Ready {
			count++
		}
	}
	return count
}

// NotReadyNames returns the namespace/name list of DaemonSets that are not Ready.
func NotReadyNames(statuses []DaemonSetStatus) []string {
	var names []string
	for _, s := range statuses {
		if !s.Ready {
			names = append(names, fmt.Sprintf("%s/%s", s.DaemonSet.Namespace, s.DaemonSet.Name))
		}
	}
	return names
}