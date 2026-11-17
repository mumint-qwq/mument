package diagnostics

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	EnvEnabled        = "QUANTUM_DIAGNOSTICS_ENABLED"
	EnvDir            = "QUANTUM_DIAGNOSTICS_DIR"
	EnvAllowedOrigins = "QUANTUM_DIAGNOSTICS_ALLOWED_ORIGINS"
	defaultDir        = "/home/filebrowser/data/diagnostics"
)

// Config is the environment-only configuration for the diagnostics receiver.
// It is intentionally separate from the main YAML so unsupported fields never
// leak into the stable configuration surface.
type Config struct {
	Enabled        bool
	Dir            string
	AllowedOrigins []string
}

// LoadConfig reads the three diagnostics environment variables. Parse errors
// disable diagnostics instead of failing the file service.
func LoadConfig(getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	cfg := Config{Dir: defaultDir}
	if raw := strings.TrimSpace(getenv(EnvEnabled)); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{Enabled: false, Dir: defaultDir}, fmt.Errorf("invalid %s: %w", EnvEnabled, err)
		}
		cfg.Enabled = enabled
	}
	if raw := strings.TrimSpace(getenv(EnvDir)); raw != "" {
		cfg.Dir = raw
	}
	if cfg.Dir == "" || !filepath.IsAbs(cfg.Dir) {
		return Config{Enabled: false, Dir: defaultDir}, fmt.Errorf("%s must be an absolute path", EnvDir)
	}
	cfg.Dir = filepath.Clean(cfg.Dir)
	if strings.ContainsRune(cfg.Dir, 0) {
		return Config{Enabled: false, Dir: defaultDir}, fmt.Errorf("%s contains NUL", EnvDir)
	}
	if raw := strings.TrimSpace(getenv(EnvAllowedOrigins)); raw != "" {
		seen := map[string]bool{}
		for _, part := range strings.Split(raw, ",") {
			origin := strings.TrimSpace(part)
			if origin == "" {
				continue
			}
			if err := validateOrigin(origin); err != nil {
				return Config{Enabled: false, Dir: defaultDir}, fmt.Errorf("invalid %s entry: %w", EnvAllowedOrigins, err)
			}
			if !seen[origin] {
				seen[origin] = true
				cfg.AllowedOrigins = append(cfg.AllowedOrigins, origin)
			}
		}
	}
	return cfg, nil
}

func validateOrigin(origin string) error {
	parsed, err := url.Parse(origin)
	if err != nil {
		return err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("origin scheme must be http or https")
	}
	if parsed.Host == "" {
		return fmt.Errorf("origin host is required")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return fmt.Errorf("origin must not contain a path")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return fmt.Errorf("origin must not contain query, fragment or userinfo")
	}
	return nil
}

// OriginAllowed matches an exact configured origin. The Host comparison is
// case-insensitive per URL rules; ports remain significant.
func (c Config) OriginAllowed(origin string) bool {
	for _, allowed := range c.AllowedOrigins {
		if strings.EqualFold(allowed, origin) {
			return true
		}
	}
	return false
}

// PrepareDir validates the configured directory, creating it 0700 when absent.
// Existing directories are not chmodded and symlinks are rejected.
func PrepareDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(path, 0o700); err != nil {
			return err
		}
		info, err = os.Lstat(path)
		if err != nil {
			return err
		}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("diagnostics dir must not be a symlink")
	}
	if !info.IsDir() {
		return fmt.Errorf("diagnostics dir is not a directory")
	}
	return nil
}
