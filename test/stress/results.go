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
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// StressTestResults is the top-level JSON schema for stress test output.
type StressTestResults struct {
	Timestamp       string           `json:"timestamp"`
	GitSHA          string           `json:"git_sha"`
	TestConfig      TestConfig       `json:"test_config"`
	Latency         LatencyBreakdown `json:"latency"`
	Counts          CountResults     `json:"counts"`
	ProfileDistro   map[string]int   `json:"profile_distribution"`
	Memory          MemorySummary    `json:"memory"`
	ResourceSamples []ResourceSample `json:"resource_samples"`
	Duration        DurationResults  `json:"duration"`
}

// TestConfig captures the parameters used for a stress test run.
type TestConfig struct {
	NodeCount             int `json:"node_count"`
	NodeRate              int `json:"node_rate"`
	TimeoutMinutes        int `json:"timeout_minutes"`
	ControllerTimeout     int `json:"controller_timeout_sec"`
	MaxConcReconciles     int `json:"max_concurrent_reconciles"`
	APIConcurrency        int `json:"api_concurrency"`
	DaemonSetCount        int `json:"daemonset_count"`
	BackgroundPods        int `json:"background_pods"`
	BackgroundPodMinBytes int `json:"background_pod_min_bytes"`
	BackgroundPodMaxBytes int `json:"background_pod_max_bytes"`
}

// LatencyBreakdown captures latency percentiles broken into three phases.
type LatencyBreakdown struct {
	// EndToEnd is the total time from node creation to taint removal.
	EndToEnd LatencyPercentiles `json:"end_to_end"`
	// PodStartup is the time from node creation to all DaemonSet pods Ready.
	// Excludes never-ready nodes.
	PodStartup LatencyPercentiles `json:"pod_startup"`
	// VigilReaction is the time from pods Ready to taint removal.
	// Isolates Vigil's overhead from pod startup time.
	// Excludes never-ready and pending nodes.
	VigilReaction LatencyPercentiles `json:"vigil_reaction"`
}

// LatencyPercentiles captures p50/p95/p99 for a single latency metric.
type LatencyPercentiles struct {
	P50Ms float64 `json:"p50_ms"`
	P95Ms float64 `json:"p95_ms"`
	P99Ms float64 `json:"p99_ms"`
}

// CountResults captures node outcome counts.
type CountResults struct {
	Total   int `json:"total"`
	Success int `json:"success"`
	Timeout int `json:"timeout"`
	Pending int `json:"pending"`
}

// MemorySummary captures memory statistics at test completion.
type MemorySummary struct {
	PeakHeapAllocMB  float64 `json:"peak_heap_alloc_mb"`
	FinalHeapAllocMB float64 `json:"final_heap_alloc_mb"`
	FinalSysMB       float64 `json:"final_sys_mb"`
	TotalGCCycles    uint32  `json:"total_gc_cycles"`
	GCCPUFraction    float64 `json:"gc_cpu_fraction"`
}

// ResourceSample is a periodic snapshot of runtime resource usage.
type ResourceSample struct {
	ElapsedSec    float64 `json:"elapsed_sec"`
	HeapAllocMB   float64 `json:"heap_alloc_mb"`
	SysMB         float64 `json:"sys_mb"`
	NumGC         uint32  `json:"num_gc"`
	NumGoroutine  int     `json:"num_goroutine"`
	GCCPUFraction float64 `json:"gc_cpu_fraction"`
}

// DurationResults captures timing information.
type DurationResults struct {
	CreationSec float64 `json:"creation_sec"`
	TotalSec    float64 `json:"total_sec"`
}

// ResourceSampler collects periodic runtime resource snapshots.
type ResourceSampler struct {
	mu       sync.Mutex
	samples  []ResourceSample
	start    time.Time
	peakHeap uint64
}

// NewResourceSampler creates a sampler that records from now.
func NewResourceSampler() *ResourceSampler {
	return &ResourceSampler{
		start: time.Now(),
	}
}

// Run collects samples at the given interval until ctx is cancelled.
func (rs *ResourceSampler) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rs.sample()
		}
	}
}

func (rs *ResourceSampler) sample() {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	goroutines := runtime.NumGoroutine()

	s := ResourceSample{
		ElapsedSec:    time.Since(rs.start).Seconds(),
		HeapAllocMB:   float64(mem.Alloc) / 1024 / 1024,
		SysMB:         float64(mem.Sys) / 1024 / 1024,
		NumGC:         mem.NumGC,
		NumGoroutine:  goroutines,
		GCCPUFraction: mem.GCCPUFraction,
	}

	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.samples = append(rs.samples, s)
	if mem.Alloc > rs.peakHeap {
		rs.peakHeap = mem.Alloc
	}
}

// Samples returns collected samples and the peak heap allocation in bytes.
func (rs *ResourceSampler) Samples() ([]ResourceSample, uint64) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make([]ResourceSample, len(rs.samples))
	copy(out, rs.samples)
	return out, rs.peakHeap
}

// WriteResults writes the results as indented JSON to the given path.
func WriteResults(results *StressTestResults, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// gitSHA returns the short git SHA of the current HEAD.
func gitSHA() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}
