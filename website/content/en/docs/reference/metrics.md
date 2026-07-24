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
that node — from the reconcile that first sees the startup taint until the taint
is gone or the node is deleted. This keeps cardinality bounded under node churn,
but it means that on an idle cluster the label-vec is empty, and the Prometheus
client omits an empty label-vec from `/metrics` entirely: no samples, no `HELP`,
no `TYPE`.

Dashboards built on the per-node gauges therefore read **no data** whenever
nothing is tainted, which is indistinguishable from a broken or absent
controller.

Use the unlabeled `vigil_tracked_*` aggregates — and `vigil_tainted_nodes`, the
same aggregate expressed as a node count — for dashboard panels and alerts. They
are registered at process start, report `0` while idle, and never drop out of the
exposition. Reach for the per-node series when drilling into which specific node
is stuck.

```promql
# Nodes waiting, and how far along they are — always plots, even when idle.
vigil_tracked_ready_daemonsets / vigil_tracked_expected_daemonsets

# Drill-down: which node is short of its expected count right now?
vigil_expected_daemonsets - vigil_ready_daemonsets > 0
```

## Alerting

Recommended alert rules:

```yaml
# Alert if >10% of taint removals are timeouts
- alert: VigilTimeoutRate
  expr: |
    rate(vigil_timeout_removals_total[15m])
    / rate(vigil_successful_removals_total[15m] + vigil_timeout_removals_total[15m])
    > 0.1
  for: 5m

# Alert if controller is down
- alert: VigilControllerDown
  expr: absent(up{job="vigil-controller"})
  for: 5m
```
