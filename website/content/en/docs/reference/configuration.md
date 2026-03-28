---
title: "Configuration"
description: "All configuration options"
weight: 10
---

## Configuration File

Vigil reads configuration from a YAML file (default: `/etc/vigil/config/config.yaml`).

```yaml
# The taint key to watch for and remove
taintKey: "node.example.com/initializing"

# The taint effect to match
taintEffect: "NoSchedule"

# All known temporary startup taint keys in the cluster.
# Used for DaemonSet discovery only — Vigil does not remove these taints.
# Their respective controllers (CSI drivers, CNI plugins) handle removal.
knownStartupTaintKeys:
  - "node.example.com/initializing"
  - "cni.istio.io/not-ready"
  - "ebs.csi.aws.com/agent-not-ready"
  - "efs.csi.aws.com/agent-not-ready"

# Maximum time to wait before removing taint anyway (seconds)
timeoutSeconds: 120

# DaemonSets to exclude from readiness checks
excludeDaemonSets:
  byName:
    - namespace: kube-system
      name: slow-daemonset
```

## CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `/etc/vigil/config/config.yaml` | Config file path |
| `--metrics-bind-address` | `:8080` | Metrics endpoint |
| `--health-probe-bind-address` | `:8081` | Health/readiness probes |
| `--leader-elect` | `false` | Enable leader election |
| `--zap-log-level` | (default) | Log verbosity |

## Configuration Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `taintKey` | string | `node.nextdoor.com/initializing` | The taint key to watch and remove |
| `taintEffect` | string | `NoSchedule` | The taint effect to match |
| `knownStartupTaintKeys` | []string | `[taintKey]` | All temporary startup taint keys in the cluster (used for discovery only — Vigil does not remove these) |
| `timeoutSeconds` | int | `120` | Max wait time before forced taint removal |
| `excludeDaemonSets.byName` | []object | `[]` | DaemonSets to exclude by namespace/name |
