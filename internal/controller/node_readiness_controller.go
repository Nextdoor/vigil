package controller

import (
	"context"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nextdoor/vigil-controller/pkg/config"
)

// NodeReadinessReconciler watches nodes with a configured startup taint and
// removes the taint once all expected DaemonSet pods are Ready.
type NodeReadinessReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
	Config *config.Config
}

// Reconcile handles a single node reconciliation.
func (r *NodeReadinessReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("node", req.NamespacedName)

	var node corev1.Node
	if err := r.Get(ctx, req.NamespacedName, &node); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Check if the node has the startup taint we're watching
	hasTaint := false
	for _, taint := range node.Spec.Taints {
		if taint.Key == r.Config.TaintKey {
			hasTaint = true
			break
		}
	}

	if !hasTaint {
		return ctrl.Result{}, nil
	}

	log.Info("node has startup taint, reconciliation pending",
		"taint-key", r.Config.TaintKey,
		"node-age", node.CreationTimestamp.Time,
	)

	// TODO: Phase 2 — DaemonSet discovery
	// TODO: Phase 3 — Pod readiness checking
	// TODO: Phase 4 — Taint removal

	return ctrl.Result{}, nil
}

// SetupWithManager registers the controller with the manager.
func (r *NodeReadinessReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}).
		Named("node-readiness").
		Complete(r)
}
