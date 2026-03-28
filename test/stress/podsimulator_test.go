//go:build stress

package stress

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// BehaviorProfile defines how a simulated node's pods behave.
type BehaviorProfile struct {
	Name       string
	MinDelay   time.Duration // min time before pods become Ready
	MaxDelay   time.Duration // max time before pods become Ready
	CrashCount int           // number of crash cycles before recovery
	NeverReady bool          // pods never become Ready (timeout scenario)
}

var defaultProfiles = []BehaviorProfile{
	{Name: "immediate", MinDelay: 1 * time.Second, MaxDelay: 5 * time.Second},
	{Name: "delayed", MinDelay: 5 * time.Second, MaxDelay: 30 * time.Second},
	{Name: "crash-recover", MinDelay: 10 * time.Second, MaxDelay: 45 * time.Second, CrashCount: 2},
	{Name: "never-ready", NeverReady: true},
}

// ProfileForIndex returns a deterministic behavior profile based on node index.
// Distribution: 70% immediate, 15% delayed, 10% crash-recover, 5% never-ready.
func ProfileForIndex(idx int) BehaviorProfile {
	bucket := idx % 20
	switch {
	case bucket < 14: // 0-13: 70%
		return defaultProfiles[0]
	case bucket < 17: // 14-16: 15%
		return defaultProfiles[1]
	case bucket < 19: // 17-18: 10%
		return defaultProfiles[2]
	default: // 19: 5%
		return defaultProfiles[3]
	}
}

// PodSimulator creates and manages DaemonSet pod lifecycle for simulated nodes.
type PodSimulator struct {
	cl         client.Client
	tracker    *NodeTracker
	daemonSets []*appsv1.DaemonSet
	rng        *rand.Rand

	// Concurrency limiter: limits simultaneous API calls.
	sem chan struct{}

	wg sync.WaitGroup
}

// NewPodSimulator creates a simulator with the given DaemonSets.
func NewPodSimulator(cl client.Client, tracker *NodeTracker, dss []*appsv1.DaemonSet, concurrency int) *PodSimulator {
	return &PodSimulator{
		cl:         cl,
		tracker:    tracker,
		daemonSets: dss,
		rng:        rand.New(rand.NewSource(42)), //nolint:gosec // deterministic for reproducibility
		sem:        make(chan struct{}, concurrency),
	}
}

// SimulateNode starts the pod lifecycle for a single node in a background goroutine.
func (ps *PodSimulator) SimulateNode(ctx context.Context, nodeName string, profile BehaviorProfile) {
	ps.wg.Add(1)
	go func() {
		defer ps.wg.Done()
		ps.runNodeLifecycle(ctx, nodeName, profile)
	}()
}

// Wait blocks until all node lifecycle goroutines have completed.
func (ps *PodSimulator) Wait() {
	ps.wg.Wait()
}

func (ps *PodSimulator) runNodeLifecycle(ctx context.Context, nodeName string, profile BehaviorProfile) {
	// Create Pending pods for each DaemonSet.
	for _, ds := range ps.daemonSets {
		pod := ps.buildPod(ds, nodeName)
		ps.acquireSem(ctx)
		if err := ps.cl.Create(ctx, pod); err != nil {
			ps.releaseSem()
			continue
		}
		ps.releaseSem()
	}

	if profile.NeverReady {
		// Leave pods as Pending. Controller will hit timeout.
		return
	}

	// Simulate crash cycles.
	for range profile.CrashCount {
		crashDelay := ps.randomDuration(2*time.Second, 8*time.Second)
		if !ps.sleep(ctx, crashDelay) {
			return
		}
		// Set pods to Running but NOT Ready.
		for _, ds := range ps.daemonSets {
			ps.updatePodStatus(ctx, ds, nodeName, corev1.PodRunning, false)
		}
	}

	// Wait for readiness delay.
	delay := ps.randomDuration(profile.MinDelay, profile.MaxDelay)
	if !ps.sleep(ctx, delay) {
		return
	}

	// Set all pods to Running + Ready.
	for _, ds := range ps.daemonSets {
		ps.updatePodStatus(ctx, ds, nodeName, corev1.PodRunning, true)
	}
	ps.tracker.MarkPodsReady(nodeName, time.Now())
}

func (ps *PodSimulator) buildPod(ds *appsv1.DaemonSet, nodeName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ds.Namespace,
			Name:      fmt.Sprintf("%s-%s", ds.Name, nodeName),
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "DaemonSet",
					Name:       ds.Name,
					UID:        ds.UID,
				},
			},
		},
		Spec: corev1.PodSpec{
			NodeName:   nodeName,
			Containers: []corev1.Container{{Name: "main", Image: "example:latest"}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
		},
	}
}

func (ps *PodSimulator) updatePodStatus(ctx context.Context, ds *appsv1.DaemonSet, nodeName string, phase corev1.PodPhase, ready bool) {
	podName := fmt.Sprintf("%s-%s", ds.Name, nodeName)
	var pod corev1.Pod
	ps.acquireSem(ctx)
	defer ps.releaseSem()

	if err := ps.cl.Get(ctx, client.ObjectKey{Namespace: ds.Namespace, Name: podName}, &pod); err != nil {
		return
	}

	pod.Status.Phase = phase
	if ready {
		pod.Status.Conditions = []corev1.PodCondition{
			{Type: corev1.PodReady, Status: corev1.ConditionTrue},
		}
	} else {
		pod.Status.Conditions = []corev1.PodCondition{
			{Type: corev1.PodReady, Status: corev1.ConditionFalse},
		}
	}

	_ = ps.cl.Status().Update(ctx, &pod)
}

func (ps *PodSimulator) randomDuration(min, max time.Duration) time.Duration {
	if max <= min {
		return min
	}
	return min + time.Duration(ps.rng.Int63n(int64(max-min)))
}

func (ps *PodSimulator) sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (ps *PodSimulator) acquireSem(ctx context.Context) {
	select {
	case ps.sem <- struct{}{}:
	case <-ctx.Done():
	}
}

func (ps *PodSimulator) releaseSem() {
	<-ps.sem
}
