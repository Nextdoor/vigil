---
title: "Concepts"
description: "Understand how Vigil works"
weight: 20
---

Vigil's core job is simple: watch tainted nodes, check DaemonSet readiness, remove the taint when ready. The complexity lies in accurately determining which DaemonSets should run on each node and safely removing the taint without interfering with other startup taint controllers.
