---
title: "DaemonSet Discovery"
description: "How Vigil determines which DaemonSets should run on each node"
weight: 20
---

## Overview

Vigil auto-discovers which DaemonSets should run on each tainted node by replicating the kube-scheduler's DaemonSet scheduling logic using the upstream `k8s.io/component-helpers` package.

## Algorithm

For each DaemonSet in the cluster:

1. Build a synthetic Pod from the DaemonSet's pod template
2. Evaluate `nodeSelector` and `nodeAffinity` against the node
3. Compute the node's "steady-state" taints by stripping all configured startup taint keys
4. Check that the DaemonSet tolerates all remaining (steady-state) taints
5. If all checks pass, the DaemonSet is expected on this node

## Why Strip Startup Taints

A brand-new node has multiple startup taints (Vigil's, Istio CNI's, EBS CSI's, EFS CSI's). These are all temporary. We want to answer "will this DaemonSet run in steady state?" not "can this DaemonSet tolerate the node right now?"

Since Kubernetes taints carry no metadata indicating whether they're temporary, the list of startup taint keys is a required configuration input.

## Exclusions

DaemonSets can be excluded from readiness checks:

- **By name**: Exclude specific DaemonSets by namespace/name
- **By label**: DaemonSet owners can add a label to opt out (planned)

## Upstream Package

Vigil uses `k8s.io/component-helpers/scheduling/corev1` for:
- `nodeaffinity.GetRequiredNodeAffinity(pod).Match(node)` — evaluates nodeSelector and nodeAffinity
- `FindMatchingUntoleratedTaint(taints, tolerations, filter)` — checks toleration compatibility
