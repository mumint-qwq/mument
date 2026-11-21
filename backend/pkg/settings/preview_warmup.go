package settings

import (
	"fmt"
	"path"
	"strings"
)

type PreviewWarmupConfig struct {
	Enabled bool                 `json:"enabled"`
	Scopes  []PreviewWarmupScope `json:"scopes"`
}

type PreviewWarmupScope struct {
	Source     string `json:"source"`
	Directory  string `json:"directory"`
	MountPoint string `json:"mountPoint,omitempty"`
}

func warmupRelative(value string) bool {
	return value != "" && !strings.ContainsAny(value, "\\\x00") && !path.IsAbs(value) &&
		value == path.Clean(value) && value != ".." && !strings.HasPrefix(value, "../")
}

func WarmupContains(directory, relative string) bool {
	return directory == "." || directory == relative || strings.HasPrefix(relative, directory+"/")
}

func validatePreviewWarmup(server Server) error {
	cfg := server.PreviewWarmup
	if !cfg.Enabled {
		return nil
	}
	if server.DisablePreviews || server.DisableResize || len(cfg.Scopes) < 1 || len(cfg.Scopes) > 16 {
		return fmt.Errorf("server.previewWarmup requires previews, resizing and 1–16 scopes")
	}
	if server.CacheDirCleanup || server.IndexSqlConfig.DisableReuse {
		return fmt.Errorf("server.previewWarmup requires a retained cache and a reusable index")
	}
	for i, scope := range cfg.Scopes {
		source := server.NameToSource[scope.Source]
		if source == nil || source.Config.ResolvedRules.IndexingDisabled || !warmupRelative(scope.Directory) {
			return fmt.Errorf("server.previewWarmup scope %d requires an indexed source and clean relative directory", i+1)
		}
		if scope.MountPoint != "" && (!warmupRelative(scope.MountPoint) || !WarmupContains(scope.MountPoint, scope.Directory)) {
			return fmt.Errorf("server.previewWarmup scope %d has an invalid mount point", i+1)
		}
		for _, previous := range cfg.Scopes[:i] {
			previousRoot := path.Join(server.NameToSource[previous.Source].Path, previous.Directory)
			root := path.Join(source.Path, scope.Directory)
			if previousRoot == root || strings.HasPrefix(root, strings.TrimRight(previousRoot, "/")+"/") ||
				strings.HasPrefix(previousRoot, strings.TrimRight(root, "/")+"/") {
				return fmt.Errorf("server.previewWarmup scope %d overlaps a previous scope", i+1)
			}
		}
	}
	return nil
}

// F uses the same scope grammar but an independent switch and durable cache.
func validatePreviewReuse(server Server) error {
	if !server.PreviewReuse.Enabled {
		return nil
	}
	if server.CacheDir == "" {
		return fmt.Errorf("server.previewReuse requires an explicit persistent cacheDir")
	}
	server.PreviewWarmup = server.PreviewReuse
	if err := validatePreviewWarmup(server); err != nil {
		return fmt.Errorf("server.previewReuse: %w", err)
	}
	return nil
}
