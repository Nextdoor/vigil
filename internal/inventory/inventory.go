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
	"fmt"
	"sort"
	"sync"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DaemonSetInventory watches all DaemonSets cluster-wide and logs when
// DaemonSets are added or removed.
type DaemonSetInventory struct {
	client client.Reader
	log    logr.Logger

	mu    sync.RWMutex
	known map[string]bool // key: "namespace/name"
}

// New creates a new DaemonSetInventory.
func New(cl client.Reader, log logr.Logger) *DaemonSetInventory {
	return &DaemonSetInventory{
		client: cl,
		log:    log,
		known:  make(map[string]bool),
	}
}

// Reconcile handles a single DaemonSet event, detecting adds and removes.
func (inv *DaemonSetInventory) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	key := fmt.Sprintf("%s/%s", req.Namespace, req.Name)

	var ds appsv1.DaemonSet
	err := inv.client.Get(ctx, req.NamespacedName, &ds)
	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, err
	}

	inv.mu.Lock()
	defer inv.mu.Unlock()

	if err != nil {
		// Not found — DaemonSet was deleted.
		if inv.known[key] {
			delete(inv.known, key)
			inv.log.Info("daemonset removed", "daemonset", key, "total", len(inv.known))
		}
		return ctrl.Result{}, nil
	}

	// DaemonSet exists.
	if !inv.known[key] {
		inv.known[key] = true
		inv.log.Info("daemonset added", "daemonset", key, "total", len(inv.known))
	}

	return ctrl.Result{}, nil
}

// SetupWithManager registers the inventory controller with the manager.
func (inv *DaemonSetInventory) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&appsv1.DaemonSet{}).
		Named("daemonset-inventory").
		Complete(inv)
}

// KnownDaemonSets returns a sorted list of all tracked DaemonSet keys.
func (inv *DaemonSetInventory) KnownDaemonSets() []string {
	inv.mu.RLock()
	defer inv.mu.RUnlock()

	result := make([]string, 0, len(inv.known))
	for k := range inv.known {
		result = append(result, k)
	}
	sort.Strings(result)
	return result
}
