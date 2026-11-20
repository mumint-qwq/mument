package settings

import "fmt"

// PreviewSpaceConfig is inert when Mode is omitted. Numeric fields use MiB.
type PreviewSpaceConfig struct {
	Mode             string `json:"mode,omitempty"`
	BudgetMiB        int64  `json:"budgetMiB,omitempty"`
	MinimumFreeMiB   int64  `json:"minimumFreeMiB,omitempty"`
	ResumeFreeMiB    int64  `json:"resumeFreeMiB,omitempty"`
	StableSeconds    int    `json:"stableSeconds,omitempty"`
	CapacityEviction bool   `json:"capacityEviction,omitempty"`
}

func (c PreviewSpaceConfig) Defaults() PreviewSpaceConfig {
	if c.MinimumFreeMiB == 0 {
		c.MinimumFreeMiB = 512
	}
	if c.ResumeFreeMiB == 0 {
		c.ResumeFreeMiB = c.MinimumFreeMiB + 256
	}
	if c.StableSeconds == 0 {
		c.StableSeconds = 60
	}
	return c
}

func validatePreviewSpace(server Server) error {
	c := server.PreviewSpace
	if c == (PreviewSpaceConfig{}) {
		return nil
	}
	if c.Mode != "observe" && c.Mode != "enforce" {
		return fmt.Errorf("server.previewSpace.mode must be observe or enforce")
	}
	if server.CacheDir == "" || server.CacheDirCleanup {
		return fmt.Errorf("server.previewSpace requires an explicit cacheDir with cacheDirCleanup disabled")
	}
	const maximumMiB = 1 << 32
	if c.BudgetMiB < 0 || c.BudgetMiB > maximumMiB ||
		c.MinimumFreeMiB < 0 || c.MinimumFreeMiB > maximumMiB ||
		c.ResumeFreeMiB < 0 || c.ResumeFreeMiB > maximumMiB ||
		c.StableSeconds < 0 || c.StableSeconds > 3600 {
		return fmt.Errorf("server.previewSpace limits are out of range")
	}
	effective := c.Defaults()
	if effective.ResumeFreeMiB <= effective.MinimumFreeMiB {
		return fmt.Errorf("server.previewSpace.resumeFreeMiB must exceed minimumFreeMiB")
	}
	if c.CapacityEviction && c.Mode != "enforce" {
		return fmt.Errorf("server.previewSpace.capacityEviction requires mode enforce")
	}
	return nil
}
