// Copyright 2026 Nextdoor, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controller

import "sync"

// nodeState tracks the last-observed readiness state per node to avoid
// redundant log lines when nothing has changed between reconciles.
type nodeState struct {
	mu    sync.Mutex
	nodes map[string]nodeSnapshot
}

type nodeSnapshot struct {
	readyCount    int
	expectedCount int
}

func newNodeState() *nodeState {
	return &nodeState{nodes: make(map[string]nodeSnapshot)}
}

// observe records the current readiness state for a node and returns whether
// this is the first observation and whether the ready count changed.
func (ns *nodeState) observe(nodeName string, expected, ready int) (first, changed bool) {
	ns.mu.Lock()
	defer ns.mu.Unlock()

	prev, exists := ns.nodes[nodeName]
	snap := nodeSnapshot{
		readyCount:    ready,
		expectedCount: expected,
	}
	ns.nodes[nodeName] = snap

	if !exists {
		return true, true
	}
	return false, prev.readyCount != ready || prev.expectedCount != expected
}

// remove cleans up tracking for a node after taint removal.
func (ns *nodeState) remove(nodeName string) {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	delete(ns.nodes, nodeName)
}
