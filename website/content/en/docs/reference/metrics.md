---
title: "Metrics"
description: "Prometheus metrics catalog"
weight: 20
---

## Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `vigil_tainted_nodes` | Gauge | Nodes currently waiting for DaemonSet readiness |
| `vigil_taint_removal_duration_seconds` | Histogram | Time from node creation to taint removal |
| `vigil_successful_removals_total` | Counter | Taint removals after all DaemonSets Ready |
| `vigil_timeout_removals_total` | Counter | Taint removals due to timeout |
| `vigil_expected_daemonsets` | Gauge (by node) | Expected DaemonSets per node |
| `vigil_ready_daemonsets` | Gauge (by node) | Ready DaemonSet pods per node |
| `vigil_tracked_expected_daemonsets` | Gauge | `vigil_expected_daemonsets` summed across all tracked nodes |
| `vigil_tracked_ready_daemonsets` | Gauge | `vigil_ready_daemonsets` summed across all tracked nodes |
| `vigil_reconcile_errors_total` | Counter | Reconciliation errors |
| `vigil_discovery_duration_seconds` | Histogram | Time to evaluate scheduling rules |
| `vigil_timeout_blocking_daemonset_total` | Counter (by ds) | Which DaemonSet blocked at timeout |

## Per-node vs. aggregate gauges

The per-node gauges carry a `node` label and exist only while vigil is tracking
that node, disappearing once the taint is gone or the node is deleted. They also
start late, and not together: `vigil_expected_daemonsets` appears once DaemonSet
discovery reports a count, and `vigil_ready_daemonsets` only after a readiness
check succeeds — so a node whose readiness check keeps failing has an expected
series and no ready series at all.

This keeps cardinality bounded under node churn, but it means that on an idle
cluster the label-vec is empty, and the Prometheus client omits an empty
label-vec from `/metrics` entirely: no samples, no `HELP`, no `TYPE`.

Dashboards built on the per-node gauges therefore read **no data** whenever
nothing is tainted, which is indistinguishable from a broken or absent
controller.

Use the unlabeled `vigil_tracked_*` aggregates and `vigil_tainted_nodes` for
dashboard panels and alerts. They are registered at process start, report `0`
while idle, and never drop out of the exposition. Reach for the per-node series
when drilling into which specific node is stuck.

`vigil_tainted_nodes` counts a node from the moment its taint is seen, which is
before DaemonSet discovery has reported. A node whose discovery keeps failing
counts there while contributing `0` to both sums — so read a progress ratio of
`0` alongside a non-zero node count as "not yet known", not as "no DaemonSets
are Ready".

The default deployment is two replicas with leader election. Both serve
`/metrics`, but only the leader ever sets these gauges, so the standby reports a
permanent `0`. Wrap queries in `max without (pod, instance) (...)` to collapse
the pair down to the leader's value — without it, panels plot two series and
alerts fire on the standby.

```promql
# How many nodes vigil is waiting on — always plots, even when idle.
max without (pod, instance) (vigil_tainted_nodes)

# Progress across those nodes. clamp_min keeps the idle case at 0: PromQL
# follows IEEE 754, so a bare 0/0 is NaN and renders as a gap.
max without (pod, instance) (
  vigil_tracked_ready_daemonsets / clamp_min(vigil_tracked_expected_daemonsets, 1)
)

# Drill-down: which node is short of its expected count right now?
# "or 0 *" substitutes a zero for a node that has no ready series yet, so it is
# still listed instead of being dropped by the vector match.
max without (pod, instance) (
  vigil_expected_daemonsets
    - (vigil_ready_daemonsets or 0 * vigil_expected_daemonsets)
) > 0
```

## Alerting

Recommended alert rules:

```yaml
# Alert if >10% of taint removals are timeouts.
# rate() is applied per counter, then summed over the replicas: the standby's
# counters never move, so the sum is the leader's rate.
- alert: VigilTimeoutRate
  expr: |
    sum without (pod, instance) (rate(vigil_timeout_removals_total[15m]))
    / sum without (pod, instance) (
        rate(vigil_successful_removals_total[15m])
        + rate(vigil_timeout_removals_total[15m])
      )
    > 0.1
  for: 5m

# Alert if controller is down
- alert: VigilControllerDown
  expr: absent(up{job="vigil-controller"})
  for: 5m
```
