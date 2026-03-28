package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// TaintedNodes tracks the number of nodes currently waiting for DaemonSet readiness.
	TaintedNodes = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "vigil_tainted_nodes",
		Help: "Number of nodes currently waiting for DaemonSet readiness.",
	})

	// TaintRemovalDuration tracks the time from node creation to taint removal.
	TaintRemovalDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "vigil_taint_removal_duration_seconds",
		Help:    "Time from node creation to taint removal.",
		Buckets: []float64{5, 10, 15, 20, 30, 45, 60, 90, 120, 180, 300},
	})

	// SuccessfulRemovals counts taint removals after all DaemonSets are Ready.
	SuccessfulRemovals = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vigil_successful_removals_total",
		Help: "Taint removals after all DaemonSets are Ready.",
	})

	// TimeoutRemovals counts taint removals due to timeout.
	TimeoutRemovals = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vigil_timeout_removals_total",
		Help: "Taint removals due to timeout.",
	})

	// ReconcileErrors counts reconciliation errors.
	ReconcileErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vigil_reconcile_errors_total",
		Help: "Reconciliation errors.",
	})

	// ExpectedDaemonSets tracks the number of expected DaemonSets per node.
	ExpectedDaemonSets = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vigil_expected_daemonsets",
		Help: "Number of expected DaemonSets per node.",
	}, []string{"node"})

	// ReadyDaemonSets tracks the number of Ready DaemonSet pods per node.
	ReadyDaemonSets = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vigil_ready_daemonsets",
		Help: "Number of Ready DaemonSet pods per node.",
	}, []string{"node"})

	// DiscoveryDuration tracks the time to evaluate DaemonSet scheduling rules.
	DiscoveryDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "vigil_discovery_duration_seconds",
		Help:    "Time to evaluate DaemonSet scheduling rules.",
		Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
	})

	// TimeoutBlockingDaemonSet tracks which DaemonSet was not Ready when timeout fired.
	TimeoutBlockingDaemonSet = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vigil_timeout_blocking_daemonset_total",
		Help: "Which DaemonSet was not Ready when the timeout fired.",
	}, []string{"daemonset_namespace", "daemonset_name"})
)

func init() {
	metrics.Registry.MustRegister(
		TaintedNodes,
		TaintRemovalDuration,
		SuccessfulRemovals,
		TimeoutRemovals,
		ReconcileErrors,
		ExpectedDaemonSets,
		ReadyDaemonSets,
		DiscoveryDuration,
		TimeoutBlockingDaemonSet,
	)
}
