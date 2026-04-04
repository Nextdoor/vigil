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

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNodeState_FirstObservation(t *testing.T) {
	ns := newNodeState()
	first, changed := ns.observe("node-1", 5, 2)
	assert.True(t, first)
	assert.True(t, changed)
}

func TestNodeState_SameState(t *testing.T) {
	ns := newNodeState()
	ns.observe("node-1", 5, 2)

	first, changed := ns.observe("node-1", 5, 2)
	assert.False(t, first)
	assert.False(t, changed, "same ready count should not be reported as changed")
}

func TestNodeState_ReadyCountChanged(t *testing.T) {
	ns := newNodeState()
	ns.observe("node-1", 5, 2)

	first, changed := ns.observe("node-1", 5, 3)
	assert.False(t, first)
	assert.True(t, changed, "ready count changed from 2 to 3")
}

func TestNodeState_ExpectedCountChanged(t *testing.T) {
	ns := newNodeState()
	ns.observe("node-1", 5, 2)

	first, changed := ns.observe("node-1", 6, 2)
	assert.False(t, first)
	assert.True(t, changed, "expected count changed from 5 to 6")
}

func TestNodeState_Remove(t *testing.T) {
	ns := newNodeState()
	ns.observe("node-1", 5, 2)
	ns.remove("node-1")

	first, changed := ns.observe("node-1", 5, 2)
	assert.True(t, first, "after remove, next observe should be first")
	assert.True(t, changed)
}

func TestNodeState_IndependentNodes(t *testing.T) {
	ns := newNodeState()
	ns.observe("node-1", 5, 2)
	ns.observe("node-2", 3, 1)

	// node-1 unchanged, node-2 changed
	_, changed1 := ns.observe("node-1", 5, 2)
	_, changed2 := ns.observe("node-2", 3, 2)
	assert.False(t, changed1)
	assert.True(t, changed2)
}