package main

import (
	"flag"
	"os"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/nextdoor/vigil/internal/controller"
	"github.com/nextdoor/vigil/internal/discovery"
	"github.com/nextdoor/vigil/internal/inventory"
	"github.com/nextdoor/vigil/internal/readiness"
	"github.com/nextdoor/vigil/internal/taintremoval"
	"github.com/nextdoor/vigil/pkg/config"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
}

func main() {
	var configFile string
	var metricsAddr string
	var probeAddr string
	var enableLeaderElection bool
	var leaseDuration time.Duration
	var renewDeadline time.Duration
	var retryPeriod time.Duration

	flag.StringVar(&configFile, "config", "/etc/vigil/config/config.yaml",
		"Path to the controller configuration file.")
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080",
		"The address the metrics endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081",
		"The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager.")
	flag.DurationVar(&leaseDuration, "leader-election-lease-duration", 15*time.Second,
		"Duration that non-leader candidates will wait to force acquire leadership.")
	flag.DurationVar(&renewDeadline, "leader-election-renew-deadline", 10*time.Second,
		"Duration the acting leader will retry refreshing leadership before giving up.")
	flag.DurationVar(&retryPeriod, "leader-election-retry-period", 2*time.Second,
		"Duration between leader election retry attempts.")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// Validate leader election timing constraints
	if renewDeadline >= leaseDuration {
		setupLog.Error(nil, "leader-election-renew-deadline must be less than leader-election-lease-duration",
			"renew-deadline", renewDeadline, "lease-duration", leaseDuration)
		os.Exit(1)
	}
	if retryPeriod >= renewDeadline {
		setupLog.Error(nil, "leader-election-retry-period must be less than leader-election-renew-deadline",
			"retry-period", retryPeriod, "renew-deadline", renewDeadline)
		os.Exit(1)
	}

	// Load configuration
	cfg, err := config.Load(configFile)
	if err != nil {
		if _, statErr := os.Stat(configFile); os.IsNotExist(statErr) {
			setupLog.Info("config file not found, using defaults", "config-file", configFile)
			cfg = config.NewDefault()
		} else {
			setupLog.Error(err, "failed to load configuration", "config-file", configFile)
			os.Exit(1)
		}
	}

	setupLog.Info("loaded configuration",
		"taint-key", cfg.TaintKey,
		"taint-effect", cfg.TaintEffect,
		"timeout-seconds", cfg.TimeoutSeconds,
		"dry-run", cfg.DryRun,
		"max-concurrent-reconciles", cfg.MaxConcurrentReconciles,
	)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress:        probeAddr,
		LeaderElection:                enableLeaderElection,
		LeaderElectionID:              "vigil-controller.nextdoor.com",
		LeaseDuration:                 &leaseDuration,
		RenewDeadline:                 &renewDeadline,
		RetryPeriod:                   &retryPeriod,
		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	dsDiscovery := discovery.New(
		mgr.GetClient(),
		ctrl.Log.WithName("discovery"),
		cfg,
	)

	podReadiness := readiness.New(
		mgr.GetClient(),
		ctrl.Log.WithName("readiness"),
	)

	taintRemover := taintremoval.New(
		mgr.GetAPIReader(),
		mgr.GetClient(),
		ctrl.Log.WithName("taint-removal"),
	)

	if err = (&controller.NodeReadinessReconciler{
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		Log:          ctrl.Log.WithName("node-readiness"),
		Config:       cfg,
		Discovery:    dsDiscovery,
		Readiness:    podReadiness,
		TaintRemover: taintRemover,
		Recorder:     mgr.GetEventRecorder("vigil-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "NodeReadiness")
		os.Exit(1)
	}

	dsInventory := inventory.New(
		mgr.GetClient(),
		ctrl.Log.WithName("daemonset-inventory"),
	)
	if err = dsInventory.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "DaemonSetInventory")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager",
		"max-concurrent-reconciles", cfg.MaxConcurrentReconciles,
		"leader-election", enableLeaderElection,
	)

	// Monitor leader election acquisition time when leader election is enabled.
	// Logs warnings at escalating intervals so operators can detect stalled
	// lease acquisition during rolling updates (see #21).
	if enableLeaderElection {
		go monitorLeaseAcquisition(mgr.Elected(), leaseDuration)
	}

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// monitorLeaseAcquisition logs warnings when leader lease acquisition is
// taking longer than expected. This surfaces leadership gaps caused by rapid
// rolling updates where multiple ReplicaSet generations churn through lease
// holders (see issue #21).
func monitorLeaseAcquisition(elected <-chan struct{}, leaseDuration time.Duration) {
	leaseLog := ctrl.Log.WithName("leader-election")
	start := time.Now()
	warnInterval := 2 * leaseDuration
	ticker := time.NewTicker(warnInterval)
	defer ticker.Stop()

	for {
		select {
		case <-elected:
			elapsed := time.Since(start)
			if elapsed > warnInterval {
				leaseLog.Info("leader lease acquired after extended wait",
					"elapsed", elapsed.Round(time.Millisecond))
			}
			return
		case <-ticker.C:
			elapsed := time.Since(start)
			if elapsed > 4*leaseDuration {
				leaseLog.Error(nil, "leader lease acquisition is critically delayed — no controller is watching nodes",
					"elapsed", elapsed.Round(time.Millisecond),
					"lease-duration", leaseDuration)
			} else {
				leaseLog.Info("WARNING: leader lease acquisition is taking longer than expected",
					"elapsed", elapsed.Round(time.Millisecond),
					"lease-duration", leaseDuration)
			}
		}
	}
}
