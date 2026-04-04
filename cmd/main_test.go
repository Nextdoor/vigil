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

package main

import (
	"testing"
	"time"
)

func TestMonitorLeaseAcquisition_FastAcquisition(t *testing.T) {
	// Lease acquired before the warning interval — should return quickly
	// with no warnings.
	elected := make(chan struct{})
	close(elected) // already elected

	done := make(chan struct{})
	go func() {
		monitorLeaseAcquisition(elected, 15*time.Second)
		close(done)
	}()

	select {
	case <-done:
		// Success — returned without blocking
	case <-time.After(time.Second):
		t.Fatal("monitorLeaseAcquisition did not return promptly for immediate election")
	}
}

func TestMonitorLeaseAcquisition_DelayedAcquisition(t *testing.T) {
	// Use very short durations so the test runs fast.
	// leaseDuration=10ms → warnInterval=20ms, ticker fires at 20ms.
	// We close elected at ~50ms so the goroutine should have ticked at
	// least once and then returned.
	elected := make(chan struct{})
	leaseDuration := 10 * time.Millisecond

	done := make(chan struct{})
	go func() {
		monitorLeaseAcquisition(elected, leaseDuration)
		close(done)
	}()

	// Let the ticker fire a couple of times, then signal election
	time.Sleep(50 * time.Millisecond)
	close(elected)

	select {
	case <-done:
		// Success
	case <-time.After(time.Second):
		t.Fatal("monitorLeaseAcquisition did not return after election signal")
	}
}