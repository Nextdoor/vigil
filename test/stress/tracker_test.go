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
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NodeRecord tracks the lifecycle of a single simulated node.
type NodeRecord struct {
	CreatedAt      time.Time
	Profile        string // "immediate", "delayed", "crash-recover", "never-ready"
	PodsReadyAt    time.Time
	TaintRemovedAt time.Time
}

// RemovalLatency returns how long it took from node creation to taint removal.
func (nr *NodeRecord) RemovalLatency() time.Duration {
	if nr.TaintRemovedAt.IsZero() {
		return 0
	}
	return nr.TaintRemovedAt.Sub(nr.CreatedAt)
}

// PodStartupLatency returns how long it took for all DaemonSet pods to become Ready.
// Returns 0 for never-ready nodes (pods never became Ready).
func (nr *NodeRecord) PodStartupLatency() time.Duration {
	if nr.PodsReadyAt.IsZero() || nr.CreatedAt.IsZero() {
		return 0
	}
	return nr.PodsReadyAt.Sub(nr.CreatedAt)
}

// VigilReactionTime returns how long after pods were Ready the taint was removed.
// This isolates Vigil's overhead from pod startup time.
// Returns 0 for never-ready nodes or nodes still pending.
func (nr *NodeRecord) VigilReactionTime() time.Duration {
	if nr.PodsReadyAt.IsZero() || nr.TaintRemovedAt.IsZero() {
		return 0
	}
	return nr.TaintRemovedAt.Sub(nr.PodsReadyAt)
}

// NodeTracker provides thread-safe tracking of node outcomes.
type NodeTracker struct {
	mu       sync.RWMutex
	nodes    map[string]*NodeRecord
	taintKey string
	doneCh   chan struct{} // closed when all nodes are terminal
}

// NewNodeTracker creates a tracker for the given taint key.
func NewNodeTracker(taintKey string) *NodeTracker {
	return &NodeTracker{
		nodes:    make(map[string]*NodeRecord),
		taintKey: taintKey,
		doneCh:   make(chan struct{}),
	}
}

// Register records a new node with its creation time and behavior profile.
func (nt *NodeTracker) Register(name string, createdAt time.Time, profile string) {
	nt.mu.Lock()
	defer nt.mu.Unlock()
	nt.nodes[name] = &NodeRecord{
		CreatedAt: createdAt,
		Profile:   profile,
	}
}

// MarkPodsReady records when all DS pods for a node became Ready.
func (nt *NodeTracker) MarkPodsReady(name string, at time.Time) {
	nt.mu.Lock()
	defer nt.mu.Unlock()
	if rec, ok := nt.nodes[name]; ok {
		rec.PodsReadyAt = at
	}
}

// Total returns the total number of registered nodes.
func (nt *NodeTracker) Total() int {
	nt.mu.RLock()
	defer nt.mu.RUnlock()
	return len(nt.nodes)
}

// TaintRemovedCount returns how many nodes have had their taint removed.
func (nt *NodeTracker) TaintRemovedCount() int {
	nt.mu.RLock()
	defer nt.mu.RUnlock()
	count := 0
	for _, rec := range nt.nodes {
		if !rec.TaintRemovedAt.IsZero() {
			count++
		}
	}
	return count
}

// PendingCount returns nodes that still have the taint.
func (nt *NodeTracker) PendingCount() int {
	return nt.Total() - nt.TaintRemovedCount()
}

// SuccessCount returns nodes whose pods became Ready AND taint was removed.
func (nt *NodeTracker) SuccessCount() int {
	nt.mu.RLock()
	defer nt.mu.RUnlock()
	count := 0
	for _, rec := range nt.nodes {
		if !rec.TaintRemovedAt.IsZero() && !rec.PodsReadyAt.IsZero() {
			count++
		}
	}
	return count
}

// TimeoutCount returns nodes where taint was removed but pods never became Ready
// (these are the "never-ready" profile nodes that hit controller timeout).
func (nt *NodeTracker) TimeoutCount() int {
	nt.mu.RLock()
	defer nt.mu.RUnlock()
	count := 0
	for _, rec := range nt.nodes {
		if !rec.TaintRemovedAt.IsZero() && rec.PodsReadyAt.IsZero() {
			count++
		}
	}
	return count
}

// ProfileCount returns how many nodes have the given profile.
func (nt *NodeTracker) ProfileCount(profile string) int {
	nt.mu.RLock()
	defer nt.mu.RUnlock()
	count := 0
	for _, rec := range nt.nodes {
		if rec.Profile == profile {
			count++
		}
	}
	return count
}

// ProfileDistribution returns a map of profile name to node count.
func (nt *NodeTracker) ProfileDistribution() map[string]int {
	nt.mu.RLock()
	defer nt.mu.RUnlock()
	return nt.profileDistributionLocked()
}

// profileDistributionLocked returns profile counts without acquiring the lock.
func (nt *NodeTracker) profileDistributionLocked() map[string]int {
	dist := make(map[string]int)
	for _, rec := range nt.nodes {
		dist[rec.Profile]++
	}
	return dist
}

// computePercentiles returns p50, p95, p99 from a slice of durations.
func computePercentiles(latencies []time.Duration) (p50, p95, p99 time.Duration) {
	if len(latencies) == 0 {
		return 0, 0, 0
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	pct := func(p float64) time.Duration {
		idx := int(math.Ceil(p*float64(len(latencies)))) - 1
		if idx < 0 {
			idx = 0
		}
		return latencies[idx]
	}
	return pct(0.50), pct(0.95), pct(0.99)
}

// RemovalLatencyPercentiles returns p50, p95, p99 for end-to-end taint removal latency
// (node creation to taint removal).
func (nt *NodeTracker) RemovalLatencyPercentiles() (p50, p95, p99 time.Duration) {
	nt.mu.RLock()
	defer nt.mu.RUnlock()
	var latencies []time.Duration
	for _, rec := range nt.nodes {
		if l := rec.RemovalLatency(); l > 0 {
			latencies = append(latencies, l)
		}
	}
	return computePercentiles(latencies)
}

// PodStartupPercentiles returns p50, p95, p99 for pod startup latency
// (node creation to all DaemonSet pods Ready). Excludes never-ready nodes.
func (nt *NodeTracker) PodStartupPercentiles() (p50, p95, p99 time.Duration) {
	nt.mu.RLock()
	defer nt.mu.RUnlock()
	var latencies []time.Duration
	for _, rec := range nt.nodes {
		if l := rec.PodStartupLatency(); l > 0 {
			latencies = append(latencies, l)
		}
	}
	return computePercentiles(latencies)
}

// VigilReactionPercentiles returns p50, p95, p99 for Vigil's reaction time
// (pods Ready to taint removal). Excludes never-ready and pending nodes.
func (nt *NodeTracker) VigilReactionPercentiles() (p50, p95, p99 time.Duration) {
	nt.mu.RLock()
	defer nt.mu.RUnlock()
	var latencies []time.Duration
	for _, rec := range nt.nodes {
		if l := rec.VigilReactionTime(); l > 0 {
			latencies = append(latencies, l)
		}
	}
	return computePercentiles(latencies)
}

// Done returns a channel that is closed when all registered nodes are terminal.
func (nt *NodeTracker) Done() <-chan struct{} {
	return nt.doneCh
}

// PollForTaintRemoval runs a background loop checking nodes for taint removal.
// It updates records and closes doneCh when all nodes are terminal.
func (nt *NodeTracker) PollForTaintRemoval(ctx context.Context, cl client.Reader, expectedTotal int) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			nt.checkTaints(ctx, cl)

			removed := nt.TaintRemovedCount()
			total := nt.Total()

			if total >= expectedTotal && removed >= total {
				close(nt.doneCh)
				return
			}
		}
	}
}

func (nt *NodeTracker) checkTaints(ctx context.Context, cl client.Reader) {
	nt.mu.RLock()
	pending := make([]string, 0, len(nt.nodes))
	for name, rec := range nt.nodes {
		if rec.TaintRemovedAt.IsZero() {
			pending = append(pending, name)
		}
	}
	nt.mu.RUnlock()

	// Check pending nodes in batches.
	for _, name := range pending {
		var node corev1.Node
		if err := cl.Get(ctx, client.ObjectKey{Name: name}, &node); err != nil {
			continue
		}
		hasTaint := false
		for _, t := range node.Spec.Taints {
			if t.Key == nt.taintKey {
				hasTaint = true
				break
			}
		}
		if !hasTaint {
			nt.mu.Lock()
			if rec, ok := nt.nodes[name]; ok && rec.TaintRemovedAt.IsZero() {
				rec.TaintRemovedAt = time.Now()
			}
			nt.mu.Unlock()
		}
	}
}

// PrintSummary logs the final results.
func (nt *NodeTracker) PrintSummary() string {
	nt.mu.RLock()
	defer nt.mu.RUnlock()

	total := len(nt.nodes)
	removed := 0
	success := 0
	timeout := 0
	pending := 0
	for _, rec := range nt.nodes {
		switch {
		case rec.TaintRemovedAt.IsZero():
			pending++
		case rec.PodsReadyAt.IsZero():
			timeout++
			removed++
		default:
			success++
			removed++
		}
	}

	p50, p95, p99 := nt.RemovalLatencyPercentiles()

	profiles := nt.profileDistributionLocked()

	var nodesByProfile string
	for p, c := range profiles {
		nodesByProfile += fmt.Sprintf("    %-15s %d\n", p, c)
	}

	return fmt.Sprintf(`
========================================
  STRESS TEST RESULTS
========================================
  Total nodes:     %d
  Taint removed:   %d (%.1f%%)
  - Successful:    %d (%.1f%%)
  - Timeout:       %d (%.1f%%)
  - Pending:       %d

  Removal latency:
    p50: %v
    p95: %v
    p99: %v

  Nodes by profile:
%s========================================
`,
		total,
		removed, pct(removed, total),
		success, pct(success, total),
		timeout, pct(timeout, total),
		pending,
		p50, p95, p99,
		nodesByProfile,
	)
}

func pct(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) / float64(total) * 100
}

// ProgressLine returns a one-line status string.
func (nt *NodeTracker) ProgressLine() string {
	total := nt.Total()
	removed := nt.TaintRemovedCount()
	pending := total - removed
	return fmt.Sprintf("nodes=%d removed=%d pending=%d", total, removed, pending)
}

// WaitForNodes polls until expectedTotal nodes are registered with a creation timestamp.
func (nt *NodeTracker) WaitForNodes(ctx context.Context, cl client.Reader, taintKey string, expectedTotal int) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var nodeList corev1.NodeList
			if err := cl.List(ctx, &nodeList, client.MatchingLabels{"stress-test": "true"}); err != nil {
				continue
			}
			for i := range nodeList.Items {
				node := &nodeList.Items[i]
				nt.mu.RLock()
				_, exists := nt.nodes[node.Name]
				nt.mu.RUnlock()
				if !exists {
					hasTaint := false
					for _, t := range node.Spec.Taints {
						if t.Key == taintKey {
							hasTaint = true
							break
						}
					}
					if hasTaint {
						// Will be registered by the main test — skip
						continue
					}
				}
			}

			// Check done condition
			removed := nt.TaintRemovedCount()
			total := nt.Total()
			if total >= expectedTotal && removed >= total {
				return
			}
		}
	}
}

// WaitWithProgress prints periodic updates while waiting for completion.
func (nt *NodeTracker) WaitWithProgress(ctx context.Context, printInterval time.Duration) {
	ticker := time.NewTicker(printInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-nt.doneCh:
			return
		case <-ticker.C:
			fmt.Printf("  [progress] %s\n", nt.ProgressLine())
		}
	}
}

// MetricsSummary returns counts for use in test assertions.
type MetricsSummary struct {
	Total     int
	Removed   int
	Success   int
	Timeout   int
	Pending   int
	CreatedAt []metav1.Time
}