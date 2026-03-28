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

	// StartupTaintKeys is the list of ALL startup taint keys present in this cluster.
	// These are stripped from the node's taint list when evaluating DaemonSet
	// tolerations, so that discovery answers "will this DS run in steady state?"
	StartupTaintKeys []string `mapstructure:"startupTaintKeys"`

	// TimeoutSeconds is the maximum time to wait before removing the taint anyway.
	TimeoutSeconds int `mapstructure:"timeoutSeconds"`

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
		StartupTaintKeys: []string{
			"node.nextdoor.com/initializing",
		},
		TimeoutSeconds: 120,
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
