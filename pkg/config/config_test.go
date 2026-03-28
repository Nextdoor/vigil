package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewDefault(t *testing.T) {
	cfg := NewDefault()
	assert.Equal(t, "node.nextdoor.com/initializing", cfg.TaintKey)
	assert.Equal(t, "NoSchedule", cfg.TaintEffect)
	assert.Equal(t, 120, cfg.TimeoutSeconds)
	assert.Len(t, cfg.StartupTaintKeys, 1)
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/config.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config file not found")
}

func TestLoad_ValidConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	content := []byte(`
taintKey: "custom.taint/key"
taintEffect: "NoExecute"
timeoutSeconds: 60
startupTaintKeys:
  - "custom.taint/key"
  - "cni.istio.io/not-ready"
excludeDaemonSets:
  byName:
    - namespace: kube-system
      name: slow-ds
`)
	require.NoError(t, os.WriteFile(configPath, content, 0644))

	cfg, err := Load(configPath)
	require.NoError(t, err)
	assert.Equal(t, "custom.taint/key", cfg.TaintKey)
	assert.Equal(t, "NoExecute", cfg.TaintEffect)
	assert.Equal(t, 60, cfg.TimeoutSeconds)
	assert.Len(t, cfg.StartupTaintKeys, 2)
	assert.Len(t, cfg.ExcludeDaemonSets.ByName, 1)
	assert.Equal(t, "slow-ds", cfg.ExcludeDaemonSets.ByName[0].Name)
}
