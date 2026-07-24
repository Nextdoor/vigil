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

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// reset clears tracked state between tests. The gauges are package-level, so
// tests here must not run in parallel with each other.
func reset(t *testing.T) {
	t.Helper()
	trackedNodes.mu.Lock()
	ExpectedDaemonSets.Reset()
	ReadyDaemonSets.Reset()
	trackedNodes.nodes = make(map[string]nodeCounts)
	trackedNodes.publishLocked()
	trackedNodes.mu.Unlock()
}

// TestAggregateGaugesExposedWhenIdle is the acceptance check for the "no data
// vs. broken" ambiguity: with nothing tracked, the per-node vecs contribute
// nothing to the exposition, but the aggregates must still be scrapeable at 0.
func TestAggregateGaugesExposedWhenIdle(t *testing.T) {
	reset(t)

	families, err := ctrlmetrics.Registry.Gather()
	require.NoError(t, err)

	names := make(map[string]bool, len(families))
	for _, f := range families {
		names[f.GetName()] = true
	}

	assert.True(t, names["vigil_tracked_expected_daemonsets"],
		"aggregate gauge must be exposed with no nodes tracked")
	assert.True(t, names["vigil_tracked_ready_daemonsets"],
		"aggregate gauge must be exposed with no nodes tracked")

	// The per-node vecs are absent while empty — the behavior the aggregates
	// exist to compensate for. Asserted so a future change to client_golang's
	// empty-vec handling surfaces here rather than silently making the
	// aggregates redundant.
	assert.False(t, names["vigil_expected_daemonsets"],
		"empty per-node vec is omitted from the exposition")
	assert.False(t, names["vigil_ready_daemonsets"],
		"empty per-node vec is omitted from the exposition")

	assert.Equal(t, 0.0, promtestutil.ToFloat64(TrackedExpectedDaemonSets))
	assert.Equal(t, 0.0, promtestutil.ToFloat64(TrackedReadyDaemonSets))
}

func TestAggregateGaugesSumAcrossNodes(t *testing.T) {
	reset(t)

	SetNodeExpected("node-a", 5)
	SetNodeReady("node-a", 3)
	SetNodeExpected("node-b", 4)
	SetNodeReady("node-b", 4)

	assert.Equal(t, 9.0, promtestutil.ToFloat64(TrackedExpectedDaemonSets))
	assert.Equal(t, 7.0, promtestutil.ToFloat64(TrackedReadyDaemonSets))

	// Per-node series stay in place alongside the aggregates.
	assert.Equal(t, 5.0, promtestutil.ToFloat64(ExpectedDaemonSets.WithLabelValues("node-a")))
	assert.Equal(t, 4.0, promtestutil.ToFloat64(ReadyDaemonSets.WithLabelValues("node-b")))
}

func TestSetNodeExpectedBeforeReady(t *testing.T) {
	reset(t)

	// The reconciler learns the expected count before the ready count, and
	// bails out between the two if the readiness check errors. The half-updated
	// node must still contribute its expected count.
	SetNodeExpected("node-a", 6)

	assert.Equal(t, 6.0, promtestutil.ToFloat64(TrackedExpectedDaemonSets))
	assert.Equal(t, 0.0, promtestutil.ToFloat64(TrackedReadyDaemonSets))
}

func TestSetNodeOverwritesPreviousCounts(t *testing.T) {
	reset(t)

	SetNodeExpected("node-a", 5)
	SetNodeReady("node-a", 1)
	SetNodeReady("node-a", 4)

	assert.Equal(t, 5.0, promtestutil.ToFloat64(TrackedExpectedDaemonSets),
		"expected count must not accumulate across updates")
	assert.Equal(t, 4.0, promtestutil.ToFloat64(TrackedReadyDaemonSets),
		"ready count must replace, not add to, the previous value")
}

func TestForgetNodeDropsSeriesAndAggregate(t *testing.T) {
	reset(t)

	SetNodeExpected("node-a", 5)
	SetNodeReady("node-a", 3)
	SetNodeExpected("node-b", 4)
	SetNodeReady("node-b", 2)

	ForgetNode("node-a")

	assert.Equal(t, 4.0, promtestutil.ToFloat64(TrackedExpectedDaemonSets),
		"forgotten node must stop contributing to the aggregate")
	assert.Equal(t, 2.0, promtestutil.ToFloat64(TrackedReadyDaemonSets))
	assert.Equal(t, 1, promtestutil.CollectAndCount(ExpectedDaemonSets),
		"only node-b's per-node series should remain")

	ForgetNode("node-b")

	assert.Equal(t, 0.0, promtestutil.ToFloat64(TrackedExpectedDaemonSets))
	assert.Equal(t, 0.0, promtestutil.ToFloat64(TrackedReadyDaemonSets))
	assert.Equal(t, 0, promtestutil.CollectAndCount(ExpectedDaemonSets))
	assert.Equal(t, 0, promtestutil.CollectAndCount(ReadyDaemonSets))
}

func TestForgetNodeUnknownNodeIsNoop(t *testing.T) {
	reset(t)

	SetNodeExpected("node-a", 5)
	SetNodeReady("node-a", 3)

	ForgetNode("never-tracked")

	assert.Equal(t, 5.0, promtestutil.ToFloat64(TrackedExpectedDaemonSets))
	assert.Equal(t, 3.0, promtestutil.ToFloat64(TrackedReadyDaemonSets))
}

// TestAggregateGaugeExposition pins the wire format. The gauge type matters:
// a "_total"-style counter suffix would have made these look like counters to
// PromQL and to promlint.
func TestAggregateGaugeExposition(t *testing.T) {
	reset(t)

	SetNodeExpected("node-a", 2)
	SetNodeReady("node-a", 1)

	expected := `
# HELP vigil_tracked_expected_daemonsets Expected DaemonSets summed across all nodes currently waiting for DaemonSet readiness.
# TYPE vigil_tracked_expected_daemonsets gauge
vigil_tracked_expected_daemonsets 2
`
	require.NoError(t, promtestutil.CollectAndCompare(
		TrackedExpectedDaemonSets, strings.NewReader(expected)))
}

// TestConcurrentUpdates guards the aggregate arithmetic under the reconciler's
// concurrent workers (MaxConcurrentReconciles defaults to 10).
func TestConcurrentUpdates(t *testing.T) {
	reset(t)

	const nodes = 50
	var wg sync.WaitGroup
	for i := range nodes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("node-%d", i)
			SetNodeExpected(name, 2)
			SetNodeReady(name, 1)
		}(i)
	}
	wg.Wait()

	assert.Equal(t, float64(nodes*2), promtestutil.ToFloat64(TrackedExpectedDaemonSets))
	assert.Equal(t, float64(nodes), promtestutil.ToFloat64(TrackedReadyDaemonSets))
}
