---
title: Vigil Controller
---

{{< blocks/cover title="Vigil" image_anchor="top" height="med" color="primary" >}}
<p class="lead mt-4">Node Readiness Controller for DaemonSet-Aware Startup Taints</p>
<a class="btn btn-lg btn-primary me-3 mb-4" href="{{< relref "docs/getting-started" >}}">
Get Started <i class="fas fa-arrow-right ms-2"></i>
</a>
<a class="btn btn-lg btn-secondary me-3 mb-4" href="https://github.com/nextdoor/vigil-controller">
View on GitHub <i class="fab fa-github ms-2"></i>
</a>
{{< /blocks/cover >}}

{{% blocks/lead color="dark" %}}
Vigil watches new Kubernetes nodes, waits for all expected DaemonSet pods to become Ready, and then removes the startup taint — ensuring workloads are only scheduled when the node has accurate resource accounting.
{{% /blocks/lead %}}

{{% blocks/section color="white" type="row" %}}

{{% blocks/feature icon="fa-solid fa-shield-halved" title="Prevents Scheduler Races" %}}
Eliminates the race condition where workload pods are scheduled before DaemonSet pods consume their resources, preventing `OutOfcpu` and `OutOfmemory` rejections.
{{% /blocks/feature %}}

{{% blocks/feature icon="fa-solid fa-magnifying-glass" title="Auto-Discovery" %}}
Automatically discovers which DaemonSets should run on each node using upstream Kubernetes scheduling predicates. Zero per-DaemonSet configuration required.
{{% /blocks/feature %}}

{{% blocks/feature icon="fa-solid fa-puzzle-piece" title="Kubernetes Native" %}}
Runs as a standard controller-runtime controller with Helm installation. Uses Karpenter's `startupTaints` feature — no custom CRDs required.
{{% /blocks/feature %}}

{{% /blocks/section %}}

{{% blocks/section color="light" %}}
## Quick Start

```bash
helm repo add vigil-controller https://oss.nextdoor.com/vigil-controller
helm repo update
helm install vigil vigil-controller/vigil-controller \
  --namespace vigil-system \
  --create-namespace
```
{{% /blocks/section %}}
