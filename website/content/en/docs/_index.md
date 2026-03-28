---
title: "Documentation"
description: "Documentation for the Vigil node readiness controller"
weight: 20
---

Vigil is a Kubernetes controller that prevents workload scheduling on new nodes until all expected DaemonSet pods are running and Ready. It uses Karpenter's `startupTaints` mechanism to gate node readiness, eliminating the scheduler cache race condition that causes pod rejections during node startup.

## Getting Started

Install Vigil in your cluster and configure Karpenter startup taints. The [Getting Started]({{< relref "getting-started" >}}) guide covers prerequisites, Helm installation, and verification.

## Concepts

Understand how Vigil works:

- [Architecture]({{< relref "concepts/architecture" >}}) — Controller design, informers, reconciliation loop
- [DaemonSet Discovery]({{< relref "concepts/daemonset-discovery" >}}) — How Vigil determines which DaemonSets should run on each node

## Reference

- [Configuration]({{< relref "reference/configuration" >}}) — All configuration options
- [Metrics]({{< relref "reference/metrics" >}}) — Prometheus metrics catalog
- [Helm Chart]({{< relref "reference/helm-chart" >}}) — Helm values reference
