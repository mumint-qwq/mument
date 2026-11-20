package settings

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPreviewSpaceValidation(t *testing.T) {
	server := Server{CacheDir: "/synthetic"}
	require.NoError(t, validatePreviewSpace(server))
	server.PreviewSpace = PreviewSpaceConfig{Mode: "observe"}
	require.NoError(t, validatePreviewSpace(server))
	effective := server.PreviewSpace.Defaults()
	require.Equal(t, int64(512), effective.MinimumFreeMiB)
	require.Equal(t, int64(768), effective.ResumeFreeMiB)
	require.Equal(t, 60, effective.StableSeconds)
	server.PreviewSpace.CapacityEviction = true
	require.Error(t, validatePreviewSpace(server))
	server.PreviewSpace.Mode = "enforce"
	require.NoError(t, validatePreviewSpace(server))
	server.PreviewSpace.ResumeFreeMiB = 500
	require.Error(t, validatePreviewSpace(server))
	server.PreviewSpace.ResumeFreeMiB = 768
	server.CacheDirCleanup = true
	require.Error(t, validatePreviewSpace(server))
}
