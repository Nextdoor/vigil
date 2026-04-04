# Vigil Controller

Node Readiness Controller for DaemonSet-Aware Startup Taints.

Vigil watches new Kubernetes nodes with a configured startup taint, auto-discovers which DaemonSets should run on each node, waits for all expected DaemonSet pods to become Ready, and then removes the startup taint — allowing workload scheduling to begin safely.

## Problem

When Karpenter (or any node provisioner) launches a new node, workload schedulers can place pods before DaemonSet pods have started. The workload scheduler doesn't account for DaemonSet resource consumption, causing pod rejections (`OutOfcpu` / `OutOfmemory`).

## How It Works

1. Karpenter applies a `startupTaint` to new nodes
2. Vigil watches for nodes with this taint
3. Vigil auto-discovers expected DaemonSets using upstream scheduling predicates
4. Vigil monitors DaemonSet pod readiness on each node
5. Once all expected DaemonSet pods are Ready, Vigil removes the taint
6. Workload scheduling proceeds with accurate resource accounting

## Installation

```bash
helm repo add vigil https://oss.nextdoor.com/vigil
helm repo update
helm install vigil vigil/vigil-controller \
  --namespace vigil-system \
  --create-namespace
```

## Documentation

Full documentation is available at [oss.nextdoor.com/vigil](https://oss.nextdoor.com/vigil/).

## Development

See [DEVELOPMENT.md](DEVELOPMENT.md) for local development setup.

## License

Copyright 2026 Nextdoor, Inc. Licensed under the [Apache License, Version 2.0](LICENSE).
