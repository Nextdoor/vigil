package discovery

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	schedhelper "k8s.io/component-helpers/scheduling/corev1"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nextdoor/vigil/pkg/config"
	"github.com/nextdoor/vigil/pkg/metrics"
)

// DaemonSetDiscovery determines which DaemonSets should run on a given node.
type DaemonSetDiscovery struct {
	client client.Reader
	log    logr.Logger
	config *config.Config
}

// New creates a new DaemonSetDiscovery instance.
func New(cl client.Reader, log logr.Logger, cfg *config.Config) *DaemonSetDiscovery {
	return &DaemonSetDiscovery{
		client: cl,
		log:    log,
		config: cfg,
	}
}

// ExpectedDaemonSets returns the list of DaemonSets that should run on the given node
// in steady state (after all startup taints have been removed).
func (d *DaemonSetDiscovery) ExpectedDaemonSets(ctx context.Context, node *corev1.Node) ([]appsv1.DaemonSet, error) {
	start := time.Now()
	defer func() {
		metrics.DiscoveryDuration.Observe(time.Since(start).Seconds())
	}()

	var dsList appsv1.DaemonSetList
	if err := d.client.List(ctx, &dsList); err != nil {
		return nil, fmt.Errorf("listing daemonsets: %w", err)
	}

	// Compute steady-state taints: node taints minus all configured startup taint keys.
	steadyStateTaints := steadyStateTaints(node.Spec.Taints, d.config.StartupTaintKeys)

	// Build exclusion lookup.
	excludeByName := buildNameExclusionSet(d.config.ExcludeDaemonSets.ByName)
	excludeByLabel, err := buildLabelSelector(d.config.ExcludeDaemonSets.ByLabel)
	if err != nil {
		return nil, fmt.Errorf("parsing label exclusion selector: %w", err)
	}

	var expected []appsv1.DaemonSet
	for i := range dsList.Items {
		ds := &dsList.Items[i]

		// Check name exclusions.
		key := fmt.Sprintf("%s/%s", ds.Namespace, ds.Name)
		if excludeByName[key] {
			d.log.V(1).Info("excluding daemonset by name", "daemonset", key)
			continue
		}

		// Check label exclusions.
		if excludeByLabel != nil && excludeByLabel.Matches(labels.Set(ds.Labels)) {
			d.log.V(1).Info("excluding daemonset by label", "daemonset", key)
			continue
		}

		// Build a synthetic pod from the DaemonSet's pod template.
		pod := syntheticPod(ds)

		// Evaluate nodeSelector + nodeAffinity.
		if !matchesNodeAffinity(pod, node) {
			continue
		}

		// Check that the DaemonSet tolerates all steady-state taints.
		if !toleratesSteadyStateTaints(pod, steadyStateTaints) {
			continue
		}

		expected = append(expected, *ds)
	}

	return expected, nil
}

// steadyStateTaints returns the node's taints with all startup taint keys removed.
// This represents the node's taint state after all startup controllers have finished.
func steadyStateTaints(taints []corev1.Taint, startupKeys []string) []corev1.Taint {
	keySet := make(map[string]bool, len(startupKeys))
	for _, k := range startupKeys {
		keySet[k] = true
	}

	var result []corev1.Taint
	for _, t := range taints {
		if !keySet[t.Key] {
			result = append(result, t)
		}
	}
	return result
}

// syntheticPod builds a minimal Pod from a DaemonSet's pod template for scheduling evaluation.
func syntheticPod(ds *appsv1.DaemonSet) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ds.Namespace,
			Name:      ds.Name + "-synthetic",
		},
		Spec: ds.Spec.Template.Spec,
	}
}

// matchesNodeAffinity evaluates both nodeSelector and nodeAffinity.requiredDuringScheduling
// against the node.
func matchesNodeAffinity(pod *corev1.Pod, node *corev1.Node) bool {
	requiredAffinity := nodeaffinity.GetRequiredNodeAffinity(pod)
	matches, err := requiredAffinity.Match(node)
	if err != nil {
		// On error, conservatively include the DaemonSet.
		return true
	}
	return matches
}

// toleratesSteadyStateTaints checks if the pod tolerates all non-startup taints on the node.
func toleratesSteadyStateTaints(pod *corev1.Pod, taints []corev1.Taint) bool {
	// Pass nil filter to check all taints. The startup taints have already been
	// stripped from the list, so what remains are long-lived taints that the
	// DaemonSet must tolerate to run in steady state.
	_, hasUntolerated := schedhelper.FindMatchingUntoleratedTaint(
		taints,
		pod.Spec.Tolerations,
		nil,
	)
	return !hasUntolerated
}

// buildNameExclusionSet creates a lookup set from DaemonSet name exclusions.
func buildNameExclusionSet(refs []config.DaemonSetRef) map[string]bool {
	if len(refs) == 0 {
		return nil
	}
	set := make(map[string]bool, len(refs))
	for _, ref := range refs {
		set[fmt.Sprintf("%s/%s", ref.Namespace, ref.Name)] = true
	}
	return set
}

// buildLabelSelector parses the label exclusion config into a selector.
func buildLabelSelector(sel *config.LabelSelector) (labels.Selector, error) {
	if sel == nil || (len(sel.MatchLabels) == 0 && len(sel.MatchExpressions) == 0) {
		return nil, nil
	}

	// Convert to metav1.LabelSelector and use the standard conversion.
	metaSel := &metav1.LabelSelector{
		MatchLabels: sel.MatchLabels,
	}
	for _, expr := range sel.MatchExpressions {
		metaSel.MatchExpressions = append(metaSel.MatchExpressions, metav1.LabelSelectorRequirement{
			Key:      expr.Key,
			Operator: metav1.LabelSelectorOperator(expr.Operator),
			Values:   expr.Values,
		})
	}

	return metav1.LabelSelectorAsSelector(metaSel)
}
