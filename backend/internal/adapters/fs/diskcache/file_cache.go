// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package diskcache

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/fileutils"
	"github.com/gtsteffaniak/go-logger/logger"
)

// Cache interface for caching operations
type Cache interface {
	Get(key string) ([]byte, error)
	Set(key string, value []byte) error
}

// FileCache struct for file-based caching
type FileCache struct {
	dir   string
	locks [64]sync.Mutex
}

func NewFileCache(dir string) (*FileCache, error) {
	cacheDir := filepath.Join(dir, "diskcache")

	// Migrate existing cache files from old structure (if any)
	if err := migrateOldCacheStructure(dir, cacheDir); err != nil {
		logger.Warningf("failed to migrate old cache structure: %v\n", err)
	}

	// Create the cache directory immediately with proper permissions
	if err := os.MkdirAll(cacheDir, fileutils.PermDir); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}
	return &FileCache{dir: cacheDir}, nil
}

// migrateOldCacheStructure moves cache files from old structure (dir/a/...) to new structure (dir/diskcache/a/...)
func migrateOldCacheStructure(oldDir, newDir string) error {
	// Read the old cache directory
	entries, err := os.ReadDir(oldDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Directory doesn't exist yet, nothing to migrate
		}
		return err
	}

	migrated := 0
	for _, entry := range entries {
		// Only migrate single-character directories (cache hash directories like a, b, c, etc.)
		if !entry.IsDir() || len(entry.Name()) != 1 {
			continue
		}

		// Skip if it's already the diskcache directory
		if entry.Name() == "d" && filepath.Join(oldDir, entry.Name()) == newDir {
			continue
		}

		oldPath := filepath.Join(oldDir, entry.Name())
		newPath := filepath.Join(newDir, entry.Name())

		// Create parent directory in new location
		if err := os.MkdirAll(newDir, fileutils.PermDir); err != nil {
			return err
		}

		// Move the directory
		if err := os.Rename(oldPath, newPath); err != nil {
			// If rename fails (e.g., cross-device), skip this one
			continue
		}
		migrated++
	}

	return nil
}

// Store publishes a cache value atomically: the bytes are written and synced to
// a temporary file in the same directory, then renamed over the final name.
// Readers therefore see either the previous complete value or the new complete
// value, never a partially written preview. A failed write removes its own
// temporary file and leaves any existing usable value untouched.
func (f *FileCache) Store(ctx context.Context, key string, value []byte) error {
	mu := f.keyLock(key)
	mu.Lock()
	defer mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	fileName := f.getFileName(key)
	dir := filepath.Dir(fileName)
	if err := os.MkdirAll(dir, fileutils.PermDir); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".preview-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	removeTemp := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if _, err := tmp.Write(value); err != nil {
		removeTemp()
		return err
	}
	if err := tmp.Sync(); err != nil {
		removeTemp()
		return err
	}
	if err := tmp.Chmod(fileutils.PermFile); err != nil {
		removeTemp()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, fileName); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// HasSized checks the ledger's physical result without loading thumbnail bytes.
func (f *FileCache) HasSized(key string, size int64) (bool, error) {
	info, err := os.Lstat(f.getFileName(key))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Mode().IsRegular() && info.Size() == size, nil
}

func (f *FileCache) Load(ctx context.Context, key string) (value []byte, exist bool, err error) {
	r, ok, err := f.open(key)
	if err != nil || !ok {
		return nil, ok, err
	}
	defer r.Close()

	value, err = io.ReadAll(r)
	if err != nil {
		return nil, false, err
	}
	return value, true, nil
}

// LoadSized bounds reads for ledger results. A size mismatch returns no bytes
// with exist=true so the caller can invalidate its publication record.
func (f *FileCache) LoadSized(ctx context.Context, key string, size int64) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	r, ok, err := f.open(key)
	if err != nil || !ok {
		return nil, ok, err
	}
	defer r.Close()
	info, err := r.Stat()
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() || info.Size() != size {
		return nil, true, nil
	}
	data, err := io.ReadAll(io.LimitReader(r, size+1))
	return data, err == nil, err
}

func (f *FileCache) Delete(ctx context.Context, key string) error {
	mu := f.keyLock(key)
	mu.Lock()
	defer mu.Unlock()

	fileName := f.getFileName(key)
	if err := os.Remove(fileName); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (f *FileCache) open(key string) (*os.File, bool, error) {
	fileName := f.getFileName(key)
	file, err := os.Open(fileName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}

	return file, true, nil
}

// Stripes keep write/delete serialization bounded as new keys are cached.
func (f *FileCache) keyLock(key string) *sync.Mutex {
	hash := sha1.Sum([]byte(key))
	return &f.locks[int(hash[0])%len(f.locks)]
}

func (f *FileCache) getFileName(key string) string {
	return filepath.Join(f.dir, RelativePath(key))
}

// RelativePath is the physical layout shared by cache I/O and read-only inventory.
func RelativePath(key string) string {
	sum := sha1.Sum([]byte(key))
	hash := hex.EncodeToString(sum[:])
	return filepath.Join(hash[:1], hash[1:3], hash)
}
