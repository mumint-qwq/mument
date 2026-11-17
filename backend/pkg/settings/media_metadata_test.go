package settings

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMediaResolvedMetadataWorkers(t *testing.T) {
	tests := []struct {
		name   string
		value  int
		expect int
	}{
		{name: "unset uses default", value: 0, expect: DefaultMetadataWorkers},
		{name: "negative uses default", value: -4, expect: DefaultMetadataWorkers},
		{name: "explicit one", value: 1, expect: 1},
		{name: "explicit within cap", value: 5, expect: 5},
		{name: "above cap is clamped", value: 99, expect: MaxMetadataWorkers},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			media := Media{MetadataWorkers: tc.value}
			if got := media.ResolvedMetadataWorkers(); got != tc.expect {
				t.Fatalf("ResolvedMetadataWorkers(%d) = %d, want %d", tc.value, got, tc.expect)
			}
		})
	}
}

func TestMediaResolvedMetadataTimeout(t *testing.T) {
	tests := []struct {
		name   string
		value  int
		expect time.Duration
	}{
		{name: "unset uses default", value: 0, expect: DefaultMetadataTimeout},
		{name: "negative uses default", value: -3, expect: DefaultMetadataTimeout},
		{name: "explicit seconds", value: 3, expect: 3 * time.Second},
		{name: "above cap is clamped", value: 9999, expect: MaxMetadataTimeout},
		{name: "overflowing value is clamped", value: 1 << 40, expect: MaxMetadataTimeout},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			media := Media{MetadataTimeoutSeconds: tc.value}
			if got := media.ResolvedMetadataTimeout(); got != tc.expect {
				t.Fatalf("ResolvedMetadataTimeout(%d) = %s, want %s", tc.value, got, tc.expect)
			}
		})
	}
}

func TestConfigLoadMetadataMediaFields(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte("server:\n  sources:\n    - path: \".\"\nintegrations:\n  media:\n    metadataWorkers: 5\n    metadataTimeoutSeconds: 25\n")
	if err := os.WriteFile(configFile, content, 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := loadConfigWithDefaults(configFile, true); err != nil {
		t.Fatalf("load config: %v", err)
	}
	media := Config.Integrations.Media
	if media.MetadataWorkers != 5 {
		t.Fatalf("MetadataWorkers = %d, want 5", media.MetadataWorkers)
	}
	if media.MetadataTimeoutSeconds != 25 {
		t.Fatalf("MetadataTimeoutSeconds = %d, want 25", media.MetadataTimeoutSeconds)
	}
	if got := media.ResolvedMetadataTimeout(); got != 25*time.Second {
		t.Fatalf("resolved timeout = %s, want 25s", got)
	}
}

func TestSetDefaultsIncludesMetadataLimits(t *testing.T) {
	defaults := SetDefaults(true)
	media := defaults.Integrations.Media
	if media.MetadataWorkers != DefaultMetadataWorkers {
		t.Fatalf("default MetadataWorkers = %d, want %d", media.MetadataWorkers, DefaultMetadataWorkers)
	}
	if media.MetadataTimeoutSeconds != int(DefaultMetadataTimeout/time.Second) {
		t.Fatalf("default MetadataTimeoutSeconds = %d, want %d", media.MetadataTimeoutSeconds, int(DefaultMetadataTimeout/time.Second))
	}
}
