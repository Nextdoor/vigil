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

package config

import (
	"fmt"
	"os"

	"github.com/spf13/viper"
)

// Config holds the controller configuration.
type Config struct {
	// TaintKey is the taint key to watch for and remove.
	TaintKey string `mapstructure:"taintKey"`

	// TaintEffect is the taint effect to match.
	TaintEffect string `mapstructure:"taintEffect"`

	// KnownStartupTaintKeys lists ALL temporary startup taint keys used in the cluster.
	//
	// Vigil only removes the taint specified by TaintKey. However, new nodes often
	// have additional temporary taints set by other controllers (e.g., CSI drivers,
	// CNI plugins). These taints are removed by their respective controllers — not
	// by Vigil.
	//
	// This list is used solely for DaemonSet discovery: when determining which
	// DaemonSets should run on a node in steady state, Vigil strips these taints
	// before evaluating scheduling predicates. Without this, DaemonSets that don't
	// tolerate these temporary taints would be incorrectly excluded from the
	// expected set.
	//
	// Must include TaintKey itself plus any other startup taints in the cluster.
	KnownStartupTaintKeys []string `mapstructure:"knownStartupTaintKeys"`

	// TimeoutSeconds is the maximum time to wait before removing the taint anyway.
	TimeoutSeconds int `mapstructure:"timeoutSeconds"`

	// DryRun when true logs taint removal decisions without actually removing taints.
	DryRun bool `mapstructure:"dryRun"`

	// MaxConcurrentReconciles is the number of concurrent reconciliation workers.
	MaxConcurrentReconciles int `mapstructure:"maxConcurrentReconciles"`

	// ExcludeDaemonSets configures which DaemonSets to exclude from readiness checks.
	ExcludeDaemonSets ExcludeDaemonSets `mapstructure:"excludeDaemonSets"`
}

// ExcludeDaemonSets configures DaemonSet exclusions.
type ExcludeDaemonSets struct {
	// ByName excludes specific DaemonSets by namespace/name.
	ByName []DaemonSetRef `mapstructure:"byName"`

	// ByLabel excludes DaemonSets matching a label selector.
	// DaemonSet owners can self-service opt out by adding the configured label.
	ByLabel *LabelSelector `mapstructure:"byLabel"`
}

// DaemonSetRef identifies a DaemonSet by namespace and name.
type DaemonSetRef struct {
	Namespace string `mapstructure:"namespace"`
	Name      string `mapstructure:"name"`
}

// LabelSelector defines a label-based exclusion selector.
type LabelSelector struct {
	MatchLabels      map[string]string          `mapstructure:"matchLabels"`
	MatchExpressions []LabelSelectorRequirement `mapstructure:"matchExpressions"`
}

// LabelSelectorRequirement is a single requirement for label matching.
type LabelSelectorRequirement struct {
	Key      string   `mapstructure:"key"`
	Operator string   `mapstructure:"operator"`
	Values   []string `mapstructure:"values"`
}

// NewDefault returns a Config with default values.
func NewDefault() *Config {
	return &Config{
		TaintKey:    "node.nextdoor.com/initializing",
		TaintEffect: "NoSchedule",
		KnownStartupTaintKeys: []string{
			"node.nextdoor.com/initializing",
		},
		TimeoutSeconds:          120,
		MaxConcurrentReconciles: 10,
	}
}

// Load reads configuration from the given file path.
func Load(path string) (*Config, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, fmt.Errorf("config file not found: %s", path)
	}

	v := viper.New()
	v.SetConfigFile(path)

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	cfg := NewDefault()
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("unmarshaling config: %w", err)
	}

	return cfg, nil
}
