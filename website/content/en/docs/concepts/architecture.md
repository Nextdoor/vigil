---
title: "Architecture"
description: "Controller design and data flow"
weight: 10
---

## Overview

Vigil is a single-binary Kubernetes controller built with controller-runtime. It runs one instance per cluster with leader election for high availability.

## Components

| Component | Purpose |
|-----------|---------|
| Node Informer | Watches nodes for startup taint changes |
| Pod Informer | Watches pods for DaemonSet readiness transitions |
| DaemonSet Cache | Caches DaemonSet list for scheduling rule evaluation |
| Node Reconciler | Core logic: discover expected DaemonSets, check readiness, remove taint |

## Data Flow

1. A new node appears with the configured startup taint
2. The Node Informer triggers a reconciliation
3. The reconciler evaluates all DaemonSets against the node's labels, affinity, and taints
4. For each expected DaemonSet, the reconciler checks if a Ready pod exists on the node
5. If all expected DaemonSet pods are Ready, the taint is removed
6. If not, the node is requeued for re-evaluation

## Safe Taint Removal

Vigil uses fresh API server reads (not informer cache) when removing taints, with optimistic concurrency via `resourceVersion`. This avoids the stale-cache bug that affected Istio's untaint controller.

## Timeout Fallback

A configurable timeout (default: 120s from node creation) ensures nodes are never permanently stuck. If the timeout expires, the taint is removed with a warning event.
