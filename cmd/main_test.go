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
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func strPtr(s string) *string { return &s }

// recordingSink is a log sink that remembers each entry so tests can check
// whether something was logged as Info or Error.
type recordingSink struct {
	mu      sync.Mutex
	entries []sinkEntry
}

type sinkEntry struct {
	isError bool
	msg     string
}

func (s *recordingSink) Init(logr.RuntimeInfo)          {}
func (s *recordingSink) Enabled(int) bool               { return true }
func (s *recordingSink) WithValues(...any) logr.LogSink { return s }
func (s *recordingSink) WithName(string) logr.LogSink   { return s }

func (s *recordingSink) Info(_ int, msg string, _ ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, sinkEntry{isError: false, msg: msg})
}

func (s *recordingSink) Error(_ error, msg string, _ ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, sinkEntry{isError: true, msg: msg})
}

func (s *recordingSink) errorCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.entries {
		if e.isError {
			n++
		}
	}
	return n
}

func TestMonitorLeaseAcquisition_FastAcquisition(t *testing.T) {
	// Lease acquired before the warning interval — should return quickly
	// with no warnings.
	elected := make(chan struct{})
	close(elected) // already elected

	done := make(chan struct{})
	go func() {
		monitorLeaseAcquisition(logr.Discard(), elected, 15*time.Second, nil)
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
		monitorLeaseAcquisition(logr.Discard(), elected, leaseDuration, nil)
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

func TestLeaderLeaseLive(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name       string
		lease      *coordinationv1.Lease
		wantHolder string
		wantLive   bool
	}{
		{
			name:     "nil lease",
			lease:    nil,
			wantLive: false,
		},
		{
			name:     "no holder identity",
			lease:    &coordinationv1.Lease{Spec: coordinationv1.LeaseSpec{RenewTime: &metav1.MicroTime{Time: now}}},
			wantLive: false,
		},
		{
			name:     "no renew time",
			lease:    &coordinationv1.Lease{Spec: coordinationv1.LeaseSpec{HolderIdentity: strPtr("pod-a")}},
			wantLive: false,
		},
		{
			name: "freshly renewed by another holder",
			lease: &coordinationv1.Lease{Spec: coordinationv1.LeaseSpec{
				HolderIdentity: strPtr("pod-a"),
				RenewTime:      &metav1.MicroTime{Time: now.Add(-2 * time.Second)},
			}},
			wantHolder: "pod-a",
			wantLive:   true,
		},
		{
			name: "stale renewal, leader is dead",
			lease: &coordinationv1.Lease{Spec: coordinationv1.LeaseSpec{
				HolderIdentity: strPtr("pod-a"),
				RenewTime:      &metav1.MicroTime{Time: now.Add(-30 * time.Second)},
			}},
			wantHolder: "pod-a",
			wantLive:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			holder, live := leaderLeaseLive(tt.lease, now, 15*time.Second)
			if live != tt.wantLive {
				t.Errorf("live = %v, want %v", live, tt.wantLive)
			}
			if holder != tt.wantHolder {
				t.Errorf("holder = %q, want %q", holder, tt.wantHolder)
			}
		})
	}
}

// A standby that never gets elected should not log an ERROR while another
// replica is the leader (issue #70).
func TestMonitorLeaseAcquisition_HealthyStandby(t *testing.T) {
	sink := &recordingSink{}

	var probeCalls atomic.Int32
	probe := func(context.Context) (string, bool) {
		probeCalls.Add(1)
		return "leader-pod", true // another replica is the leader
	}

	elected := make(chan struct{}) // never closes, so we stay a standby
	leaseDuration := 10 * time.Millisecond
	done := make(chan struct{})
	go func() {
		monitorLeaseAcquisition(logr.New(sink), elected, leaseDuration, probe)
		close(done)
	}()

	// Wait long enough that the old code would have logged an ERROR.
	time.Sleep(120 * time.Millisecond)

	if probeCalls.Load() == 0 {
		t.Fatal("probe was never consulted")
	}
	if n := sink.errorCount(); n != 0 {
		t.Fatalf("healthy standby emitted %d ERROR log(s), want 0", n)
	}
}

// When no leader is live the monitor should still log an ERROR, so we don't
// lose the stuck-cluster check from issue #55.
func TestMonitorLeaseAcquisition_NoLiveLeader(t *testing.T) {
	sink := &recordingSink{}

	probe := func(context.Context) (string, bool) {
		return "", false // no live leader
	}

	elected := make(chan struct{})
	leaseDuration := 10 * time.Millisecond
	done := make(chan struct{})
	go func() {
		monitorLeaseAcquisition(logr.New(sink), elected, leaseDuration, probe)
		close(done)
	}()

	// After 40ms (4 lease durations) the monitor should log an ERROR.
	time.Sleep(120 * time.Millisecond)

	if n := sink.errorCount(); n == 0 {
		t.Fatal("no-live-leader case did not emit any ERROR log")
	}
}

// End to end through the real probe: a stale lease (held by a dead pod) should
// still produce the ERROR, which is the stuck case from issue #55.
func TestMonitorLeaseAcquisition_StaleLease(t *testing.T) {
	namespace := "vigil-system"
	leaseDuration := 10 * time.Millisecond

	// A lease whose last renewal is way older than the lease duration.
	staleLease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: leaderElectionID},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity: strPtr("dead-pod"),
			RenewTime:      &metav1.MicroTime{Time: time.Now().Add(-time.Minute)},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(staleLease).Build()
	probe := newLeaseProbe(c, namespace, leaderElectionID, leaseDuration)

	sink := &recordingSink{}
	elected := make(chan struct{})
	done := make(chan struct{})
	go func() {
		monitorLeaseAcquisition(logr.New(sink), elected, leaseDuration, probe)
		close(done)
	}()

	time.Sleep(120 * time.Millisecond)

	if n := sink.errorCount(); n == 0 {
		t.Fatal("stale lease did not emit any ERROR log")
	}
}
