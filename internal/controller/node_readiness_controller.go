package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/nextdoor/vigil/internal/discovery"
	"github.com/nextdoor/vigil/internal/readiness"
	"github.com/nextdoor/vigil/pkg/config"
	"github.com/nextdoor/vigil/pkg/metrics"
)

const requeueDelay = 5 * time.Second

// NodeReadinessReconciler watches nodes with a configured startup taint and
// removes the taint once all expected DaemonSet pods are Ready.
type NodeReadinessReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	Log       logr.Logger
	Config    *config.Config
	Discovery *discovery.DaemonSetDiscovery
	Readiness *readiness.PodReadinessChecker
}

// Reconcile handles a single node reconciliation.
func (r *NodeReadinessReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("node", req.NamespacedName)

	var node corev1.Node
	if err := r.Get(ctx, req.NamespacedName, &node); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Check if the node has the startup taint we're watching.
	if !hasTaint(&node, r.Config.TaintKey) {
		return ctrl.Result{}, nil
	}

	log.Info("node has startup taint, evaluating readiness",
		"taint-key", r.Config.TaintKey,
		"node-age", node.CreationTimestamp.Time,
	)

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
		log.Info("all expected DaemonSet pods are Ready",
			"expected", len(expectedDS),
			"ready", readyCount,
		)
		// TODO: Phase 4 — Taint removal
		return ctrl.Result{}, nil
	}

	notReady := readiness.NotReadyNames(statuses)
	log.Info("waiting for DaemonSet pods to become Ready",
		"expected", len(expectedDS),
		"ready", readyCount,
		"not-ready", notReady,
	)

	return ctrl.Result{RequeueAfter: requeueDelay}, nil
}

// SetupWithManager registers the controller with the manager and sets up
// a field indexer for pod spec.nodeName and watches for pod events.
func (r *NodeReadinessReconciler) SetupWithManager(mgr ctrl.Manager) error {
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

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(r.podToNode)).
		Named("node-readiness").
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
