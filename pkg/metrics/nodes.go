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

package metrics

import "sync"

// nodeAggregate holds the per-node DaemonSet counts that back the unlabeled
// aggregate gauges.
//
// Why the aggregates exist: client_golang omits a label-vec with zero children
// from the exposition entirely — no samples, no HELP, no TYPE. On a low-churn
// cluster with nothing currently tainted, vigil_expected_daemonsets and
// vigil_ready_daemonsets are therefore absent from /metrics, so dashboards read
// "no data" and a healthy-but-idle vigil looks identical to a broken one. The
// unlabeled aggregates have no such hole: they are registered at process start
// and report 0 while idle.
//
// The map is the single source of truth for both, so the aggregates cannot
// drift from the per-node series they summarize. It also backs TaintedNodes,
// which is the same aggregate expressed as a node count.
type nodeAggregate struct {
	mu    sync.Mutex
	nodes map[string]nodeCounts
}

type nodeCounts struct {
	expected int
	ready    int
}

var trackedNodes = &nodeAggregate{nodes: make(map[string]nodeCounts)}

// SetNodeExpected records the expected-DaemonSet count for a tracked node.
func SetNodeExpected(nodeName string, expected int) {
	ExpectedDaemonSets.WithLabelValues(nodeName).Set(float64(expected))

	trackedNodes.mu.Lock()
	defer trackedNodes.mu.Unlock()
	c := trackedNodes.nodes[nodeName]
	c.expected = expected
	trackedNodes.nodes[nodeName] = c
	trackedNodes.publishLocked()
}

// SetNodeReady records the Ready-DaemonSet-pod count for a tracked node.
func SetNodeReady(nodeName string, ready int) {
	ReadyDaemonSets.WithLabelValues(nodeName).Set(float64(ready))

	trackedNodes.mu.Lock()
	defer trackedNodes.mu.Unlock()
	c := trackedNodes.nodes[nodeName]
	c.ready = ready
	trackedNodes.nodes[nodeName] = c
	trackedNodes.publishLocked()
}

// ForgetNode drops a node from the per-node series and the aggregates once it
// is no longer tracked, keeping per-node cardinality bounded under churn.
func ForgetNode(nodeName string) {
	ExpectedDaemonSets.DeleteLabelValues(nodeName)
	ReadyDaemonSets.DeleteLabelValues(nodeName)

	trackedNodes.mu.Lock()
	defer trackedNodes.mu.Unlock()
	delete(trackedNodes.nodes, nodeName)
	trackedNodes.publishLocked()
}

// publishLocked recomputes the aggregate gauges from the tracked node set. The
// set is bounded by the number of concurrently-tainted nodes, so summing on
// every update is cheaper than the bookkeeping needed to maintain running
// totals correctly across partial updates. Caller must hold mu.
func (a *nodeAggregate) publishLocked() {
	var expected, ready int
	for _, c := range a.nodes {
		expected += c.expected
		ready += c.ready
	}
	TaintedNodes.Set(float64(len(a.nodes)))
	TrackedExpectedDaemonSets.Set(float64(expected))
	TrackedReadyDaemonSets.Set(float64(ready))
}
