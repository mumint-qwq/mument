package settings

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPreviewWarmupScopeValidation(t *testing.T) {
	photos := &Source{Name: "photos", Path: "/sources/photos"}
	s := Server{NameToSource: map[string]*Source{photos.Name: photos}}
	require.NoError(t, validatePreviewWarmup(s))
	s.PreviewWarmup.Enabled = true
	require.Error(t, validatePreviewWarmup(s))
	s.PreviewWarmup.Scopes = []PreviewWarmupScope{{Source: "photos", Directory: "Pictures", MountPoint: "."}}
	require.NoError(t, validatePreviewWarmup(s))
	s.PreviewWarmup.Scopes = append(s.PreviewWarmup.Scopes, PreviewWarmupScope{Source: "photos", Directory: "Pictures-old"})
	require.NoError(t, validatePreviewWarmup(s))
	for _, dir := range []string{"", "/Pictures", "../Pictures", "Pictures/.", "Pictures//a", "Pictures/a", "Pictures", `Pictures\a`} {
		s.PreviewWarmup.Scopes[1].Directory = dir
		require.Error(t, validatePreviewWarmup(s), dir)
	}
	s.PreviewWarmup.Scopes = s.PreviewWarmup.Scopes[:1]
	s.PreviewWarmup.Scopes[0].MountPoint = "Pictures-old"
	require.Error(t, validatePreviewWarmup(s))
}

func TestPreviewReuseIndependentScopeValidation(t *testing.T) {
	photos := &Source{Name: "photos", Path: "/photos"}
	s := Server{NameToSource: map[string]*Source{"photos": photos}}
	require.NoError(t, validatePreviewReuse(s))
	s.PreviewReuse = PreviewWarmupConfig{Enabled: true, Scopes: []PreviewWarmupScope{{Source: "photos", Directory: "Pictures"}}}
	require.Error(t, validatePreviewReuse(s))
	s.CacheDir = "/cache"
	require.NoError(t, validatePreviewReuse(s))
	require.False(t, s.PreviewWarmup.Enabled)
	s.PreviewReuse.Scopes = append(s.PreviewReuse.Scopes, PreviewWarmupScope{Source: "photos", Directory: "Pictures/sub"})
	require.Error(t, validatePreviewReuse(s))
}
