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

package taintremoval

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const maxRetries = 5

// TaintRemover removes a specific taint from nodes using direct API server reads
// with optimistic concurrency retries.
type TaintRemover struct {
	// directReader bypasses the informer cache for fresh reads.
	directReader client.Reader
	// writer applies updates.
	writer client.Writer
	log    logr.Logger
}

// New creates a TaintRemover. The reader should be configured to bypass the
// informer cache (e.g. mgr.GetAPIReader()) so taint removal reads fresh state.
func New(reader client.Reader, writer client.Writer, log logr.Logger) *TaintRemover {
	return &TaintRemover{
		directReader: reader,
		writer:       writer,
		log:          log,
	}
}

// RemoveTaint removes the taint with the given key from the named node.
// It reads fresh state from the API server and retries on conflict (up to 5 times).
// Returns true if the taint was actually removed, false if the node didn't have it.
func (tr *TaintRemover) RemoveTaint(ctx context.Context, nodeName, taintKey string) (bool, error) {
	for attempt := range maxRetries {
		var node corev1.Node
		if err := tr.directReader.Get(ctx, types.NamespacedName{Name: nodeName}, &node); err != nil {
			return false, fmt.Errorf("reading node %s from API server: %w", nodeName, err)
		}

		filtered, found := removeTaintByKey(node.Spec.Taints, taintKey)
		if !found {
			tr.log.V(1).Info("taint already absent",
				"node", nodeName,
				"taint-key", taintKey,
			)
			return false, nil
		}

		node.Spec.Taints = filtered
		if err := tr.writer.Update(ctx, &node); err != nil {
			if apierrors.IsConflict(err) {
				tr.log.V(1).Info("conflict on taint removal, retrying",
					"node", nodeName,
					"attempt", attempt+1,
				)
				continue
			}
			return false, fmt.Errorf("updating node %s: %w", nodeName, err)
		}

		tr.log.Info("taint removed",
			"node", nodeName,
			"taint-key", taintKey,
		)
		return true, nil
	}

	return false, fmt.Errorf("failed to remove taint from node %s after %d retries", nodeName, maxRetries)
}

// removeTaintByKey returns a new taint slice with the given key removed.
// Returns the filtered slice and whether the key was found.
func removeTaintByKey(taints []corev1.Taint, key string) ([]corev1.Taint, bool) {
	found := false
	filtered := make([]corev1.Taint, 0, len(taints))
	for _, t := range taints {
		if t.Key == key {
			found = true
			continue
		}
		filtered = append(filtered, t)
	}
	return filtered, found
}
