//go:build stress

package stress

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"go.uber.org/zap/zapcore"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/nextdoor/vigil/internal/controller"
	"github.com/nextdoor/vigil/internal/discovery"
	"github.com/nextdoor/vigil/internal/inventory"
	"github.com/nextdoor/vigil/internal/readiness"
	"github.com/nextdoor/vigil/internal/taintremoval"
	"github.com/nextdoor/vigil/pkg/config"
)

// Environment variable configuration with defaults.
func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

const taintKey = "node.stress-test.io/initializing"

func TestStress(t *testing.T) {
	// ---------- Configuration ----------
	nodeCount := envInt("STRESS_NODE_COUNT", 10000)
	nodeRate := envInt("STRESS_NODE_RATE", 10)
	timeoutMin := envInt("STRESS_TIMEOUT_MINUTES", 30)
	controllerTimeout := envInt("STRESS_CONTROLLER_TIMEOUT_SEC", 45)
	maxReconciles := envInt("STRESS_MAX_CONCURRENT_RECONCILES", 50)
	logLevel := envInt("STRESS_LOG_LEVEL", 0)
	apiConcurrency := envInt("STRESS_API_CONCURRENCY", 150)

	t.Logf("Stress test config: nodes=%d rate=%d/s timeout=%dm controller-timeout=%ds workers=%d",
		nodeCount, nodeRate, timeoutMin, controllerTimeout, maxReconciles)

	deadline := time.Duration(timeoutMin) * time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	// ---------- Start envtest ----------
	// Default to warn level to keep stress test output readable.
	// STRESS_LOG_LEVEL: 0=warn (default), 1=info, 2+=debug
	zapOpts := zap.Options{}
	switch {
	case logLevel >= 2:
		zapOpts.Development = true
	case logLevel == 1:
		// info level (zap default)
	default:
		zapLevel := zapcore.WarnLevel
		zapOpts.Level = zapLevel
	}
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))

	scheme := k8sruntime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)

	t.Log("Starting envtest (API server + etcd)...")
	testEnv := &envtest.Environment{}
	restCfg, err := testEnv.Start()
	require.NoError(t, err, "envtest start failed")
	defer func() {
		t.Log("Stopping envtest...")
		require.NoError(t, testEnv.Stop())
	}()
	t.Log("envtest started")

	// ---------- Create controller-runtime manager ----------
	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: "0", // disable HTTP metrics server
		},
		HealthProbeBindAddress: "0", // disable health probes
	})
	require.NoError(t, err)

	cfg := &config.Config{
		TaintKey:    taintKey,
		TaintEffect: "NoSchedule",
		StartupTaintKeys: []string{
			taintKey,
			"node.kubernetes.io/not-ready", // envtest adds this (no kubelet)
		},
		TimeoutSeconds:          controllerTimeout,
		MaxConcurrentReconciles: maxReconciles,
	}

	dsDiscovery := discovery.New(mgr.GetClient(), ctrl.Log.WithName("discovery"), cfg)
	podReadiness := readiness.New(mgr.GetClient(), ctrl.Log.WithName("readiness"))
	taintRemover := taintremoval.New(mgr.GetAPIReader(), mgr.GetClient(), ctrl.Log.WithName("taint-removal"))

	require.NoError(t, (&controller.NodeReadinessReconciler{
		Client:       mgr.GetClient(),
		Scheme:       scheme,
		Log:          ctrl.Log.WithName("node-readiness"),
		Config:       cfg,
		Discovery:    dsDiscovery,
		Readiness:    podReadiness,
		TaintRemover: taintRemover,
		Recorder:     mgr.GetEventRecorderFor("vigil-stress-test"),
	}).SetupWithManager(mgr))

	dsInventory := inventory.New(mgr.GetClient(), ctrl.Log.WithName("inventory"))
	require.NoError(t, dsInventory.SetupWithManager(mgr))

	// ---------- Start manager ----------
	mgrCtx, mgrCancel := context.WithCancel(ctx)
	defer mgrCancel()

	go func() {
		if err := mgr.Start(mgrCtx); err != nil {
			t.Logf("Manager stopped: %v", err)
		}
	}()

	// Wait for cache sync.
	synced := mgr.GetCache().WaitForCacheSync(ctx)
	require.True(t, synced, "cache sync failed")
	t.Log("Manager cache synced")

	// ---------- Create namespaces and DaemonSets ----------
	cl := mgr.GetClient()

	// Create namespaces (envtest starts with only default + kube-system).
	for _, ns := range []string{"kube-system", "monitoring"} {
		nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		_ = cl.Create(ctx, nsObj) // ignore AlreadyExists
	}

	daemonSets := []*appsv1.DaemonSet{
		newDaemonSet("kube-system", "kube-proxy"),
		newDaemonSet("kube-system", "aws-cni"),
		newDaemonSet("monitoring", "node-exporter"),
	}

	apiReader := mgr.GetAPIReader()
	for _, ds := range daemonSets {
		require.NoError(t, cl.Create(ctx, ds))
		// Re-read via direct API reader to get server-assigned UID.
		require.NoError(t, apiReader.Get(ctx, types.NamespacedName{
			Namespace: ds.Namespace, Name: ds.Name,
		}, ds))
	}

	// Give informers time to pick up the DaemonSets.
	time.Sleep(2 * time.Second)

	// Verify DaemonSets are visible from cached client.
	var dsCheck appsv1.DaemonSetList
	require.NoError(t, cl.List(ctx, &dsCheck))
	t.Logf("Created %d DaemonSets, cached client sees %d", len(daemonSets), len(dsCheck.Items))
	require.Equal(t, len(daemonSets), len(dsCheck.Items),
		"cached client should see all DaemonSets")

	// ---------- Start resource sampling ----------
	sampler := NewResourceSampler()
	go sampler.Run(ctx, 5*time.Second)

	// ---------- Set up tracker and pod simulator ----------
	tracker := NewNodeTracker(taintKey)
	simulator := NewPodSimulator(cl, tracker, daemonSets, apiConcurrency)

	// Start taint-removal polling in background.
	go tracker.PollForTaintRemoval(ctx, mgr.GetAPIReader(), nodeCount)

	// Start progress reporting.
	go tracker.WaitWithProgress(ctx, 10*time.Second)

	// ---------- Stream nodes ----------
	t.Logf("Starting node creation: %d nodes at %d/sec...", nodeCount, nodeRate)
	interval := time.Second / time.Duration(nodeRate)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	startTime := time.Now()

	for i := range nodeCount {
		select {
		case <-ctx.Done():
			t.Fatalf("Context cancelled during node creation at node %d", i)
		case <-ticker.C:
		}

		profile := ProfileForIndex(i)
		nodeName := fmt.Sprintf("stress-node-%05d", i)

		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName,
				Labels: map[string]string{
					"stress-test":        "true",
					"kubernetes.io/os":   "linux",
					"kubernetes.io/arch": "amd64",
				},
			},
			Spec: corev1.NodeSpec{
				Taints: []corev1.Taint{
					{Key: taintKey, Effect: corev1.TaintEffectNoSchedule},
				},
			},
		}

		require.NoError(t, cl.Create(ctx, node))
		tracker.Register(nodeName, time.Now(), profile.Name)
		simulator.SimulateNode(ctx, nodeName, profile)

		if (i+1)%1000 == 0 {
			t.Logf("Created %d/%d nodes (%.0fs elapsed) | %s",
				i+1, nodeCount, time.Since(startTime).Seconds(), tracker.ProgressLine())
		}
	}

	creationDuration := time.Since(startTime)
	t.Logf("All %d nodes created in %v", nodeCount, creationDuration.Round(time.Second))

	// ---------- Wait for completion ----------
	t.Log("Waiting for all taints to be removed...")

	select {
	case <-tracker.Done():
		t.Logf("All taints removed in %v total", time.Since(startTime).Round(time.Second))
	case <-ctx.Done():
		t.Logf("Deadline reached after %v", time.Since(startTime).Round(time.Second))
	}

	// Give the simulator goroutines time to finish.
	simulatorDone := make(chan struct{})
	go func() {
		simulator.Wait()
		close(simulatorDone)
	}()
	select {
	case <-simulatorDone:
	case <-time.After(30 * time.Second):
		t.Log("Warning: simulator goroutines did not finish within 30s")
	}

	totalDuration := time.Since(startTime)

	// ---------- Results ----------
	summary := tracker.PrintSummary()
	t.Log(summary)

	p50, p95, p99 := tracker.RemovalLatencyPercentiles()
	t.Logf("Latency: p50=%v p95=%v p99=%v", p50, p95, p99)

	// ---------- Write structured results ----------
	var finalMem runtime.MemStats
	runtime.ReadMemStats(&finalMem)
	samples, peakHeap := sampler.Samples()

	results := &StressTestResults{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		GitSHA:    gitSHA(),
		TestConfig: TestConfig{
			NodeCount:         nodeCount,
			NodeRate:          nodeRate,
			TimeoutMinutes:    timeoutMin,
			ControllerTimeout: controllerTimeout,
			MaxConcReconciles: maxReconciles,
			APIConcurrency:    apiConcurrency,
			DaemonSetCount:    len(daemonSets),
		},
		Latency: LatencyResults{
			P50Ms: float64(p50.Milliseconds()),
			P95Ms: float64(p95.Milliseconds()),
			P99Ms: float64(p99.Milliseconds()),
		},
		Counts: CountResults{
			Total:   nodeCount,
			Success: tracker.SuccessCount(),
			Timeout: tracker.TimeoutCount(),
			Pending: tracker.PendingCount(),
		},
		ProfileDistro: tracker.ProfileDistribution(),
		Memory: MemorySummary{
			PeakHeapAllocMB:  float64(peakHeap) / 1024 / 1024,
			FinalHeapAllocMB: float64(finalMem.Alloc) / 1024 / 1024,
			FinalSysMB:       float64(finalMem.Sys) / 1024 / 1024,
			TotalGCCycles:    finalMem.NumGC,
			GCCPUFraction:    finalMem.GCCPUFraction,
		},
		ResourceSamples: samples,
		Duration: DurationResults{
			CreationSec: creationDuration.Seconds(),
			TotalSec:    totalDuration.Seconds(),
		},
	}

	resultsPath := "results/latest.json"
	if err := WriteResults(results, resultsPath); err != nil {
		t.Logf("Warning: failed to write results JSON: %v", err)
	} else {
		t.Logf("Results written to %s (from test/stress/)", resultsPath)
	}

	// ---------- Assertions ----------
	t.Log("Running assertions...")

	// 1. All nodes must have had their taint removed.
	assert.Equal(t, nodeCount, tracker.TaintRemovedCount(),
		"all nodes should have taint removed")

	// 2. Expected profile distribution.
	expectedNeverReady := nodeCount / 20 // 5%
	assert.Equal(t, expectedNeverReady, tracker.ProfileCount("never-ready"),
		"5%% of nodes should be never-ready profile")

	// 3. Timeout count should match never-ready profile count.
	assert.InDelta(t, expectedNeverReady, tracker.TimeoutCount(), float64(expectedNeverReady)*0.1+1,
		"timeout count should approximately match never-ready profile count")

	// 4. Success count should be everything except timeouts.
	assert.InDelta(t, nodeCount-expectedNeverReady, tracker.SuccessCount(),
		float64(nodeCount)*0.02+1,
		"success count should be ~95%%")

	// 5. Latency sanity checks.
	// p50 should be under 30s (most nodes are "immediate" with 1-5s delay).
	assert.Less(t, p50, 30*time.Second, "p50 latency should be under 30s")

	// p99 should be under the controller timeout + buffer.
	maxExpected := time.Duration(controllerTimeout)*time.Second + 30*time.Second
	assert.Less(t, p99, maxExpected,
		"p99 latency should be under controller timeout + 30s buffer")

	// 6. No pending nodes.
	assert.Zero(t, tracker.PendingCount(), "no nodes should be pending")

	// 7. Memory sanity check.
	heapMB := finalMem.Alloc / 1024 / 1024
	t.Logf("Heap: %d MB", heapMB)
	assert.Less(t, finalMem.Alloc, uint64(4*1024*1024*1024), "heap should stay under 4GB")

	t.Logf("Stress test completed in %v", totalDuration.Round(time.Second))
}

func newDaemonSet(namespace, name string) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels:    map[string]string{"app": name},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": name},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: name, Image: "example/" + name + ":latest"},
					},
				},
			},
		},
		Status: appsv1.DaemonSetStatus{
			DesiredNumberScheduled: 1,
			CurrentNumberScheduled: 1,
		},
	}
}

