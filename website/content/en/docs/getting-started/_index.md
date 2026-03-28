---
title: "Getting Started"
description: "Install and configure Vigil in your Kubernetes cluster"
weight: 10
---

## Prerequisites

- Kubernetes 1.30+
- Helm 3.x
- Karpenter (or any node provisioner that supports startup taints)
- Prometheus (for metrics collection)

## Installation

### Add the Helm Repository

```bash
helm repo add vigil https://oss.nextdoor.com/vigil
helm repo update
```

### Install

```bash
helm install vigil vigil/vigil-controller \
  --namespace vigil-system \
  --create-namespace \
  --set config.taintKey="node.example.com/initializing" \
  --set config.taintEffect="NoSchedule"
```

### Configure Karpenter Startup Taints

Add the startup taint to your Karpenter NodePool definitions:

```yaml
spec:
  template:
    spec:
      startupTaints:
        - key: node.example.com/initializing
          effect: NoSchedule
```

### Verify

```bash
# Check the controller is running
kubectl get pods -n vigil-system

# Check metrics are being exposed
kubectl port-forward -n vigil-system svc/vigil-vigil-controller-controller-manager-metrics-service 8080:8080
curl http://localhost:8080/metrics | grep vigil_
```

## Configuration

See the [Configuration Reference]({{< relref "../reference/configuration" >}}) for all available options.
