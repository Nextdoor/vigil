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
| `vigil_reconcile_errors_total` | Counter | Reconciliation errors |
| `vigil_discovery_duration_seconds` | Histogram | Time to evaluate scheduling rules |
| `vigil_timeout_blocking_daemonset_total` | Counter (by ds) | Which DaemonSet blocked at timeout |

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
