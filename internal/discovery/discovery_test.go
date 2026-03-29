package discovery

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/nextdoor/vigil/pkg/config"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	return s
}

func defaultConfig() *config.Config {
	return &config.Config{
		TaintKey:    "node.example.com/initializing",
		TaintEffect: "NoSchedule",
		KnownStartupTaintKeys: []string{
			"node.example.com/initializing",
			"cni.example.io/not-ready",
		},
		TimeoutSeconds: 120,
	}
}

func newNode(nodeLabels map[string]string, taints []corev1.Taint) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "node-1",
			Labels: nodeLabels,
		},
		Spec: corev1.NodeSpec{
			Taints: taints,
		},
	}
}

func newDaemonSet(namespace, name string, opts ...func(*appsv1.DaemonSet)) *appsv1.DaemonSet {
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": name},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: name, Image: "example/" + name + ":latest"},
					},
				},
			},
		},
	}
	for _, opt := range opts {
		opt(ds)
	}
	return ds
}

func withNodeSelector(sel map[string]string) func(*appsv1.DaemonSet) {
	return func(ds *appsv1.DaemonSet) {
		ds.Spec.Template.Spec.NodeSelector = sel
	}
}

func withNodeAffinity(req *corev1.NodeSelector) func(*appsv1.DaemonSet) {
	return func(ds *appsv1.DaemonSet) {
		ds.Spec.Template.Spec.Affinity = &corev1.Affinity{
			NodeAffinity: &corev1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: req,
			},
		}
	}
}

func withTolerations(tolerations ...corev1.Toleration) func(*appsv1.DaemonSet) {
	return func(ds *appsv1.DaemonSet) {
		ds.Spec.Template.Spec.Tolerations = tolerations
	}
}

func withLabels(dsLabels map[string]string) func(*appsv1.DaemonSet) {
	return func(ds *appsv1.DaemonSet) {
		ds.Labels = dsLabels
	}
}

func TestExpectedDaemonSets_BasicMatch(t *testing.T) {
	scheme := newScheme()
	cfg := defaultConfig()

	node := newNode(map[string]string{"kubernetes.io/os": "linux"}, []corev1.Taint{
		{Key: "node.example.com/initializing", Effect: corev1.TaintEffectNoSchedule},
	})

	ds1 := newDaemonSet("kube-system", "kube-proxy")
	ds2 := newDaemonSet("kube-system", "node-exporter")

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ds1, ds2).Build()
	d := New(cl, logr.Discard(), cfg)

	expected, err := d.ExpectedDaemonSets(context.Background(), node)
	require.NoError(t, err)
	assert.Len(t, expected, 2)
}

func TestExpectedDaemonSets_NodeSelectorMismatch(t *testing.T) {
	scheme := newScheme()
	cfg := defaultConfig()

	node := newNode(map[string]string{"kubernetes.io/os": "linux"}, nil)

	// This DS requires a GPU node.
	dsGPU := newDaemonSet("kube-system", "gpu-driver",
		withNodeSelector(map[string]string{"accelerator": "nvidia"}),
	)
	// This DS matches any linux node.
	dsGeneric := newDaemonSet("kube-system", "node-exporter",
		withNodeSelector(map[string]string{"kubernetes.io/os": "linux"}),
	)

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(dsGPU, dsGeneric).Build()
	d := New(cl, logr.Discard(), cfg)

	expected, err := d.ExpectedDaemonSets(context.Background(), node)
	require.NoError(t, err)
	assert.Len(t, expected, 1)
	assert.Equal(t, "node-exporter", expected[0].Name)
}

func TestExpectedDaemonSets_NodeAffinityMatch(t *testing.T) {
	scheme := newScheme()
	cfg := defaultConfig()

	node := newNode(map[string]string{"topology.kubernetes.io/zone": "us-west-2a"}, nil)

	dsZoneA := newDaemonSet("monitoring", "zone-monitor",
		withNodeAffinity(&corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{
					MatchExpressions: []corev1.NodeSelectorRequirement{
						{
							Key:      "topology.kubernetes.io/zone",
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{"us-west-2a", "us-west-2b"},
						},
					},
				},
			},
		}),
	)

	dsZoneC := newDaemonSet("monitoring", "zone-c-only",
		withNodeAffinity(&corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{
					MatchExpressions: []corev1.NodeSelectorRequirement{
						{
							Key:      "topology.kubernetes.io/zone",
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{"us-west-2c"},
						},
					},
				},
			},
		}),
	)

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(dsZoneA, dsZoneC).Build()
	d := New(cl, logr.Discard(), cfg)

	expected, err := d.ExpectedDaemonSets(context.Background(), node)
	require.NoError(t, err)
	assert.Len(t, expected, 1)
	assert.Equal(t, "zone-monitor", expected[0].Name)
}

func TestExpectedDaemonSets_StartupTaintStripping(t *testing.T) {
	scheme := newScheme()
	cfg := defaultConfig()

	// Node has startup taints AND a long-lived dedicated taint.
	node := newNode(nil, []corev1.Taint{
		{Key: "node.example.com/initializing", Effect: corev1.TaintEffectNoSchedule},
		{Key: "cni.example.io/not-ready", Effect: corev1.TaintEffectNoSchedule},
		{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule},
	})

	// DS that tolerates the dedicated taint — should match.
	dsGPU := newDaemonSet("kube-system", "gpu-monitor",
		withTolerations(corev1.Toleration{
			Key:      "dedicated",
			Operator: corev1.TolerationOpEqual,
			Value:    "gpu",
			Effect:   corev1.TaintEffectNoSchedule,
		}),
	)

	// DS with no tolerations — should NOT match (can't tolerate "dedicated" taint).
	dsPlain := newDaemonSet("kube-system", "plain-agent")

	// DS with Exists toleration — tolerates everything, should match.
	dsTolerant := newDaemonSet("kube-system", "everything-tolerant",
		withTolerations(corev1.Toleration{Operator: corev1.TolerationOpExists}),
	)

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(dsGPU, dsPlain, dsTolerant).Build()
	d := New(cl, logr.Discard(), cfg)

	expected, err := d.ExpectedDaemonSets(context.Background(), node)
	require.NoError(t, err)
	assert.Len(t, expected, 2)

	names := make([]string, len(expected))
	for i, ds := range expected {
		names[i] = ds.Name
	}
	assert.Contains(t, names, "gpu-monitor")
	assert.Contains(t, names, "everything-tolerant")
	assert.NotContains(t, names, "plain-agent")
}

func TestExpectedDaemonSets_ExcludeByName(t *testing.T) {
	scheme := newScheme()
	cfg := defaultConfig()
	cfg.ExcludeDaemonSets.ByName = []config.DaemonSetRef{
		{Namespace: "kube-system", Name: "slow-ds"},
	}

	node := newNode(nil, nil)

	dsKeep := newDaemonSet("kube-system", "fast-ds")
	dsExclude := newDaemonSet("kube-system", "slow-ds")

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(dsKeep, dsExclude).Build()
	d := New(cl, logr.Discard(), cfg)

	expected, err := d.ExpectedDaemonSets(context.Background(), node)
	require.NoError(t, err)
	assert.Len(t, expected, 1)
	assert.Equal(t, "fast-ds", expected[0].Name)
}

func TestExpectedDaemonSets_ExcludeByLabel(t *testing.T) {
	scheme := newScheme()
	cfg := defaultConfig()
	cfg.ExcludeDaemonSets.ByLabel = &config.LabelSelector{
		MatchExpressions: []config.LabelSelectorRequirement{
			{Key: "vigil.dev/ignore", Operator: "Exists"},
		},
	}

	node := newNode(nil, nil)

	dsKeep := newDaemonSet("kube-system", "monitored-ds")
	dsExclude := newDaemonSet("kube-system", "ignored-ds",
		withLabels(map[string]string{"vigil.dev/ignore": "true"}),
	)

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(dsKeep, dsExclude).Build()
	d := New(cl, logr.Discard(), cfg)

	expected, err := d.ExpectedDaemonSets(context.Background(), node)
	require.NoError(t, err)
	assert.Len(t, expected, 1)
	assert.Equal(t, "monitored-ds", expected[0].Name)
}

func TestExpectedDaemonSets_NoDaemonSets(t *testing.T) {
	scheme := newScheme()
	cfg := defaultConfig()
	node := newNode(nil, nil)

	cl := fake.NewClientBuilder().WithScheme(scheme).Build()
	d := New(cl, logr.Discard(), cfg)

	expected, err := d.ExpectedDaemonSets(context.Background(), node)
	require.NoError(t, err)
	assert.Empty(t, expected)
}

func TestExpectedDaemonSets_NoStartupTaintsConfigured(t *testing.T) {
	scheme := newScheme()
	cfg := defaultConfig()
	cfg.KnownStartupTaintKeys = nil // No startup taints to strip.

	// Node has a taint that the DS doesn't tolerate.
	node := newNode(nil, []corev1.Taint{
		{Key: "node.example.com/initializing", Effect: corev1.TaintEffectNoSchedule},
	})

	// DS with no tolerations — without stripping, the taint blocks it.
	ds := newDaemonSet("kube-system", "plain-ds")

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ds).Build()
	d := New(cl, logr.Discard(), cfg)

	expected, err := d.ExpectedDaemonSets(context.Background(), node)
	require.NoError(t, err)
	assert.Empty(t, expected, "DS should not match because startup taint is not stripped")
}

func TestSteadyStateTaints(t *testing.T) {
	taints := []corev1.Taint{
		{Key: "startup-a", Effect: corev1.TaintEffectNoSchedule},
		{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule},
		{Key: "startup-b", Effect: corev1.TaintEffectNoSchedule},
		{Key: "permanent", Effect: corev1.TaintEffectNoExecute},
	}

	result := steadyStateTaints(taints, []string{"startup-a", "startup-b"})
	assert.Len(t, result, 2)
	assert.Equal(t, "dedicated", result[0].Key)
	assert.Equal(t, "permanent", result[1].Key)
}

func TestSteadyStateTaints_EmptyStartupKeys(t *testing.T) {
	taints := []corev1.Taint{
		{Key: "foo", Effect: corev1.TaintEffectNoSchedule},
	}
	result := steadyStateTaints(taints, nil)
	assert.Len(t, result, 1)
}

func TestSteadyStateTaints_AllStartup(t *testing.T) {
	taints := []corev1.Taint{
		{Key: "startup-a", Effect: corev1.TaintEffectNoSchedule},
	}
	result := steadyStateTaints(taints, []string{"startup-a"})
	assert.Empty(t, result)
}

func TestMatchesNodeAffinity_NoAffinityMatchesAll(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{}}
	node := newNode(map[string]string{"foo": "bar"}, nil)
	assert.True(t, matchesNodeAffinity(pod, node))
}

func TestToleratesSteadyStateTaints_NoTaints(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{}}
	assert.True(t, toleratesSteadyStateTaints(logr.Discard(), pod, nil))
}

func TestBuildLabelSelector_Nil(t *testing.T) {
	sel, err := buildLabelSelector(nil)
	require.NoError(t, err)
	assert.Nil(t, sel)
}

func TestBuildLabelSelector_Empty(t *testing.T) {
	sel, err := buildLabelSelector(&config.LabelSelector{})
	require.NoError(t, err)
	assert.Nil(t, sel)
}

func TestBuildLabelSelector_MatchLabels(t *testing.T) {
	sel, err := buildLabelSelector(&config.LabelSelector{
		MatchLabels: map[string]string{"ignore": "true"},
	})
	require.NoError(t, err)
	require.NotNil(t, sel)

	assert.True(t, sel.Matches(labels.Set{"ignore": "true", "other": "val"}))
	assert.False(t, sel.Matches(labels.Set{"other": "val"}))
}
