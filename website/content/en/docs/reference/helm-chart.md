---
title: "Helm Chart"
description: "Helm values reference"
weight: 30
---

## Installation

```bash
helm repo add vigil-controller https://oss.nextdoor.com/vigil-controller
helm repo update
helm install vigil vigil-controller/vigil-controller \
  --namespace vigil-system \
  --create-namespace
```

## Key Values

| Value | Default | Description |
|-------|---------|-------------|
| `replicaCount` | `2` | Number of controller replicas |
| `image.repository` | `ghcr.io/nextdoor/vigil-controller` | Container image |
| `image.tag` | Chart appVersion | Image tag |
| `controllerManager.leaderElection.enabled` | `true` | Leader election |
| `controllerManager.logLevel` | `info` | Log level |
| `config.taintKey` | `node.nextdoor.com/initializing` | Taint key to watch |
| `config.taintEffect` | `NoSchedule` | Taint effect |
| `config.timeoutSeconds` | `120` | Timeout before forced removal |
| `config.startupTaintKeys` | See values.yaml | All startup taint keys |
| `serviceMonitor.enabled` | `false` | Create ServiceMonitor |
| `resources.requests.cpu` | `100m` | CPU request |
| `resources.requests.memory` | `64Mi` | Memory request |
| `resources.limits.cpu` | `500m` | CPU limit |
| `resources.limits.memory` | `128Mi` | Memory limit |

See the full [values.yaml](https://github.com/Nextdoor/vigil-controller/blob/main/charts/vigil-controller/values.yaml) for all options.
