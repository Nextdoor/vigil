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

package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlcontroller "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/nextdoor/vigil/internal/discovery"
	"github.com/nextdoor/vigil/internal/readiness"
	"github.com/nextdoor/vigil/internal/taintremoval"
	"github.com/nextdoor/vigil/pkg/config"
	"github.com/nextdoor/vigil/pkg/metrics"
)

const requeueDelay = 5 * time.Second

// NodeReadinessReconciler watches nodes with a configured startup taint and
// removes the taint once all expected DaemonSet pods are Ready.
type NodeReadinessReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	Log          logr.Logger
	Config       *config.Config
	Discovery    *discovery.DaemonSetDiscovery
	Readiness    *readiness.PodReadinessChecker
	TaintRemover *taintremoval.TaintRemover
	Recorder     events.EventRecorder

	// nodeState tracks per-node readiness to suppress redundant log lines.
	nodeState *nodeState
}

// initNodeState lazily initializes the nodeState tracker. This is called
// automatically by SetupWithManager, but also handles the case where the
// reconciler is used directly in tests without the manager.
func (r *NodeReadinessReconciler) initNodeState() {
	if r.nodeState == nil {
		r.nodeState = newNodeState()
	}
}

// Reconcile handles a single node reconciliation.
func (r *NodeReadinessReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.initNodeState()
	log := r.Log.WithValues("node", req.NamespacedName)

	var node corev1.Node
	if err := r.Get(ctx, req.NamespacedName, &node); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Check if the node has the startup taint we're watching.
	if !hasTaint(&node, r.Config.TaintKey) {
		return ctrl.Result{}, nil
	}

	nodeAge := time.Since(node.CreationTimestamp.Time).Round(time.Second)

	// Check for timeout before doing discovery.
	timedOut := r.isTimedOut(&node)

	// Discover expected DaemonSets for this node.
	expectedDS, err := r.Discovery.ExpectedDaemonSets(ctx, &node)
	if err != nil {
		metrics.ReconcileErrors.Inc()
		return ctrl.Result{}, fmt.Errorf("discovering expected daemonsets: %w", err)
	}

	metrics.ExpectedDaemonSets.WithLabelValues(node.Name).Set(float64(len(expectedDS)))

	// Check pod readiness for each expected DaemonSet.
	statuses, err := r.Readiness.CheckNode(ctx, node.Name, expectedDS)
	if err != nil {
		metrics.ReconcileErrors.Inc()
		return ctrl.Result{}, fmt.Errorf("checking pod readiness: %w", err)
	}

	readyCount := readiness.CountReady(statuses)
	metrics.ReadyDaemonSets.WithLabelValues(node.Name).Set(float64(readyCount))

	if readyCount == len(expectedDS) {
		log.Info("node ready, removing taint",
			"expected", len(expectedDS),
			"ready", readyCount,
			"node-age", nodeAge,
		)
		r.nodeState.remove(node.Name)
		return r.removeTaint(ctx, &node, "TaintRemoved",
			fmt.Sprintf("All %d expected DaemonSet pods are Ready", len(expectedDS)))
	}

	notReady := readiness.NotReadyNames(statuses)

	if timedOut {
		log.Info("timeout reached, removing taint despite not-ready DaemonSets",
			"expected", len(expectedDS),
			"ready", readyCount,
			"not-ready", notReady,
			"timeout-seconds", r.Config.TimeoutSeconds,
			"node-age", nodeAge,
		)
		metrics.TimeoutRemovals.Inc()
		for _, s := range statuses {
			if !s.Ready {
				metrics.TimeoutBlockingDaemonSet.WithLabelValues(
					s.DaemonSet.Namespace, s.DaemonSet.Name,
				).Inc()
			}
		}
		r.nodeState.remove(node.Name)
		return r.removeTaint(ctx, &node, "TaintRemovedTimeout",
			fmt.Sprintf("Timeout after %ds: %d/%d DaemonSets Ready, blocking: %v",
				r.Config.TimeoutSeconds, readyCount, len(expectedDS), notReady))
	}

	// Deduplicate waiting logs: only log at INFO when state changes.
	first, changed := r.nodeState.observe(node.Name, len(expectedDS), readyCount)

	if first {
		log.Info("tracking new node with startup taint",
			"taint-key", r.Config.TaintKey,
			"expected", len(expectedDS),
			"ready", readyCount,
			"not-ready-count", len(notReady),
			"node-age", nodeAge,
		)
		// Full not-ready list at debug level for first observation.
		log.V(1).Info("not-ready DaemonSets",
			"not-ready", notReady,
		)
		// If the node has been around for a while but we're only now seeing
		// it, it was likely waiting during a leader election gap.
		if nodeAge > 30*time.Second {
			metrics.LeadershipCatchupNodes.Inc()
			log.Info("node was waiting during leadership gap",
				"node-age", nodeAge,
			)
		}
	} else if changed {
		log.Info("DaemonSet readiness changed",
			"expected", len(expectedDS),
			"ready", readyCount,
			"not-ready-count", len(notReady),
			"node-age", nodeAge,
		)
		log.V(1).Info("not-ready DaemonSets",
			"not-ready", notReady,
		)
	} else {
		// No change — log at debug only to avoid noise.
		log.V(1).Info("waiting for DaemonSet pods to become Ready",
			"expected", len(expectedDS),
			"ready", readyCount,
			"not-ready", notReady,
			"node-age", nodeAge,
		)
	}

	return ctrl.Result{RequeueAfter: requeueDelay}, nil
}

// removeTaint handles taint removal with dry-run support, events, and metrics.
func (r *NodeReadinessReconciler) removeTaint(
	ctx context.Context,
	node *corev1.Node,
	reason, message string,
) (ctrl.Result, error) {
	eventType := corev1.EventTypeNormal
	if reason == "TaintRemovedTimeout" {
		eventType = corev1.EventTypeWarning
	}

	if r.Config.DryRun {
		r.Log.Info("dry-run: would remove taint",
			"node", node.Name,
			"reason", reason,
			"message", message,
		)
		if r.Recorder != nil {
			r.Recorder.Eventf(node, nil, eventType, reason+"DryRun", "Reconcile", "[dry-run] "+message)
		}
		return ctrl.Result{RequeueAfter: requeueDelay}, nil
	}

	removed, err := r.TaintRemover.RemoveTaint(ctx, node.Name, r.Config.TaintKey)
	if err != nil {
		metrics.ReconcileErrors.Inc()
		return ctrl.Result{}, fmt.Errorf("removing taint: %w", err)
	}

	if removed {
		duration := time.Since(node.CreationTimestamp.Time).Seconds()
		metrics.TaintRemovalDuration.Observe(duration)
		if reason == "TaintRemoved" {
			metrics.SuccessfulRemovals.Inc()
		}
		if r.Recorder != nil {
			r.Recorder.Eventf(node, nil, eventType, reason, "Reconcile", message)
		}
	}

	return ctrl.Result{}, nil
}

// isTimedOut returns true if the node has exceeded the configured timeout.
func (r *NodeReadinessReconciler) isTimedOut(node *corev1.Node) bool {
	if r.Config.TimeoutSeconds <= 0 {
		return false
	}
	age := time.Since(node.CreationTimestamp.Time)
	return age >= time.Duration(r.Config.TimeoutSeconds)*time.Second
}

// SetupWithManager registers the controller with the manager and sets up
// a field indexer for pod spec.nodeName and watches for pod events.
func (r *NodeReadinessReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.nodeState = newNodeState()

	// Add field indexer for spec.nodeName on Pods.
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&corev1.Pod{},
		readiness.NodeNameField,
		func(o client.Object) []string {
			pod := o.(*corev1.Pod)
			if pod.Spec.NodeName == "" {
				return nil
			}
			return []string{pod.Spec.NodeName}
		},
	); err != nil {
		return fmt.Errorf("setting up pod field indexer: %w", err)
	}

	maxWorkers := r.Config.MaxConcurrentReconciles
	if maxWorkers <= 0 {
		maxWorkers = 10
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(r.podToNode)).
		Named("node-readiness").
		WithOptions(ctrlcontroller.Options{MaxConcurrentReconciles: maxWorkers}).
		Complete(r)
}

// podToNode maps a pod event to a reconcile request for the pod's node.
func (r *NodeReadinessReconciler) podToNode(_ context.Context, o client.Object) []reconcile.Request {
	pod, ok := o.(*corev1.Pod)
	if !ok || pod.Spec.NodeName == "" {
		return nil
	}
	return []reconcile.Request{
		{NamespacedName: types.NamespacedName{Name: pod.Spec.NodeName}},
	}
}

// hasTaint returns true if the node has a taint with the given key.
func hasTaint(node *corev1.Node, key string) bool {
	for _, t := range node.Spec.Taints {
		if t.Key == key {
			return true
		}
	}
	return false
}