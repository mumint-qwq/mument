// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package diskcache

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/fileutils"
	"github.com/stretchr/testify/require"
)

func TestFileCache(t *testing.T) {
	ctx := context.Background()
	const (
		key            = "key"
		value          = "some text"
		newValue       = "new text"
		cacheRoot      = "cache"
		cachedFilePath = "a/62/a62f2225bf70bfaccbc7f1ef2a397836717377de"
	)

	// Set up file permissions before creating cache
	fileutils.SetFsPermissions(0644, 0755)

	// Create temporary directory for the cache
	cacheDir, err := os.MkdirTemp("", cacheRoot)
	require.NoError(t, err)
	defer os.RemoveAll(cacheDir) // Clean up

	cache, err := NewFileCache(cacheDir)
	require.NoError(t, err)

	// store new key
	// Note: NewFileCache creates a "diskcache" subdirectory, so the actual path includes it
	err = cache.Store(ctx, key, []byte(value))
	require.NoError(t, err)
	checkValue(t, ctx, cache, filepath.Join(cacheDir, "diskcache", cachedFilePath), key, value)

	// update existing key
	err = cache.Store(ctx, key, []byte(newValue))
	require.NoError(t, err)
	checkValue(t, ctx, cache, filepath.Join(cacheDir, "diskcache", cachedFilePath), key, newValue)

	// delete key
	err = cache.Delete(ctx, key)
	require.NoError(t, err)
	exists := fileExists(filepath.Join(cacheDir, "diskcache", cachedFilePath))
	require.False(t, exists)
}

func TestFileCacheStoreIsAtomicForConcurrentReaders(t *testing.T) {
	ctx := context.Background()
	fileutils.SetFsPermissions(0644, 0755)
	cacheDir := t.TempDir()
	cache, err := NewFileCache(cacheDir)
	require.NoError(t, err)
	const key = "atomic"
	first := bytes.Repeat([]byte("A"), 1<<20)
	second := bytes.Repeat([]byte("B"), 1<<20)
	require.NoError(t, cache.Store(ctx, key, first))

	stop := make(chan struct{})
	readErr := make(chan error, 1)
	go func() {
		defer close(readErr)
		for {
			select {
			case <-stop:
				return
			default:
			}
			value, found, err := cache.Load(ctx, key)
			if err != nil {
				readErr <- err
				return
			}
			if !found {
				readErr <- errors.New("cache value disappeared during replace")
				return
			}
			if !bytes.Equal(value, first) && !bytes.Equal(value, second) {
				readErr <- errors.New("reader observed a partial cache value")
				return
			}
		}
	}()
	for i := 0; i < 40; i++ {
		value := first
		if i%2 == 1 {
			value = second
		}
		require.NoError(t, cache.Store(ctx, key, value))
	}
	close(stop)
	require.NoError(t, <-readErr)
}

func TestFileCacheFailedStoreKeepsExistingValue(t *testing.T) {
	ctx := context.Background()
	fileutils.SetFsPermissions(0644, 0755)
	cacheDir := t.TempDir()
	cache, err := NewFileCache(cacheDir)
	require.NoError(t, err)
	const key = "readonly"
	oldValue := []byte("usable old preview")
	require.NoError(t, cache.Store(ctx, key, oldValue))

	finalPath := cache.getFileName(key)
	dir := filepath.Dir(finalPath)
	require.NoError(t, os.Chmod(dir, 0o555))
	defer os.Chmod(dir, 0o755)

	err = cache.Store(ctx, key, []byte("new value must not replace the old one"))
	require.Error(t, err)
	value, found, err := cache.Load(ctx, key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, oldValue, value)

	matches, err := filepath.Glob(filepath.Join(dir, ".preview-*"))
	require.NoError(t, err)
	require.Empty(t, matches, "failed store must remove its own temporary file")
}

func TestFileCacheCanceledStoreWritesNothing(t *testing.T) {
	fileutils.SetFsPermissions(0644, 0755)
	cacheDir := t.TempDir()
	cache, err := NewFileCache(cacheDir)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, cache.Store(ctx, "canceled", []byte("x")), context.Canceled)
	_, found, err := cache.Load(context.Background(), "canceled")
	require.NoError(t, err)
	require.False(t, found)
}

func checkValue(t *testing.T, ctx context.Context, cache *FileCache, fileFullPath string, key, wantValue string) {
	t.Helper()
	// check actual file content
	b, err := os.ReadFile(fileFullPath)
	require.NoError(t, err)
	require.Equal(t, wantValue, string(b))

	// check cache content
	b, ok, err := cache.Load(ctx, key)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, wantValue, string(b))
}

func fileExists(filename string) bool {
	info, err := os.Stat(filename)
	if os.IsNotExist(err) {
		return false
	}
	return !info.IsDir()
}
