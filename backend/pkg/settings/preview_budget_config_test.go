// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package settings

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPreviewMemoryBudgetDefaults(t *testing.T) {
	cfg := SetDefaults(false)
	require.Equal(t, DefaultPreviewMemoryBudgetMB, cfg.Server.PreviewMemoryBudgetMB)
	require.Equal(t, 768, DefaultPreviewMemoryBudgetMB)
}

func TestPreviewMemoryBudgetValidation(t *testing.T) {
	base := SetDefaults(false)
	base.Server.Sources = []*Source{{Path: "."}}
	previous := Config
	t.Cleanup(func() { Config = previous })
	for _, budget := range []int{0, 64, 768, MaxPreviewMemoryBudgetMB} {
		cfg := base
		cfg.Server.PreviewMemoryBudgetMB = budget
		Config = cfg
		require.NoErrorf(t, ValidateConfig(cfg), "budget %d should be accepted", budget)
	}
	for _, budget := range []int{-1, 1, 63, MaxPreviewMemoryBudgetMB + 1} {
		cfg := base
		cfg.Server.PreviewMemoryBudgetMB = budget
		Config = cfg
		require.Error(t, ValidateConfig(cfg), "budget %d should be rejected", budget)
	}
}

func TestPreviewMemoryBudgetZeroNormalizesToDefault(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "config.yaml")
	content := `server:
  previewMemoryBudgetMB: 0
  sources:
    - path: .
`
	require.NoError(t, os.WriteFile(configFile, []byte(content), 0o600))
	require.NoError(t, loadConfigWithDefaults(configFile, true))
	require.Equal(t, DefaultPreviewMemoryBudgetMB, Config.Server.PreviewMemoryBudgetMB)
}

func TestPreviewMemoryBudgetExplicitValueLoads(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "config.yaml")
	content := `server:
  previewMemoryBudgetMB: 512
  sources:
    - path: .
`
	require.NoError(t, os.WriteFile(configFile, []byte(content), 0o600))
	require.NoError(t, loadConfigWithDefaults(configFile, true))
	require.Equal(t, 512, Config.Server.PreviewMemoryBudgetMB)
}
