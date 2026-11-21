//go:build linux && !386 && !arm && !cacheledger_portable

package files

import (
	"context"
	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
)

const contentScaleIsF = true

func contentScaleConfigure(a, b string) {
	settings.Config.Server.NameToSource = map[string]*settings.Source{"scale-a": {Name: "scale-a", Path: a}, "scale-b": {Name: "scale-b", Path: b}}
	settings.Config.Server.PreviewReuse = settings.PreviewWarmupConfig{Enabled: true, Scopes: []settings.PreviewWarmupScope{{Source: "scale-a", Directory: "."}, {Source: "scale-b", Directory: "."}}}
}
func contentScaleProve(t *testing.T, s *preview.Service, file iteminfo.ExtendedFileInfo) int64 {
	t.Helper()
	out, e := s.ReuseContent(t.Context(), file, func(context.Context) (*os.File, error) { return os.Open(file.RealPath) }, func(context.Context) (os.FileInfo, error) { return os.Stat(file.RealPath) }, nil)
	require.NoError(t, e)
	require.Contains(t, []string{"proven_own", "reused"}, out.Reason)
	return out.Bytes
}

func contentScaleRetention(t *testing.T, s *preview.Service) (int, int) {
	t.Helper()
	retained, assets := 0, 0
	cursor := ""
	for {
		page, e := s.ContentLedger().ContentRetentionPage(t.Context(), cursor)
		require.NoError(t, e)
		for _, r := range page {
			assets++
			if r.OriginReferenced {
				retained++
			}
			cursor = r.Cursor
		}
		if len(page) < 16 {
			break
		}
	}
	return retained, assets
}
