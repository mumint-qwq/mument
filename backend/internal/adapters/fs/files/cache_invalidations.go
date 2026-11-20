package files

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/diskcache"
	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/fileutils"
	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
)

// removeResource attaches evidence to filesystem success before index
// maintenance. Partial RemoveAll never confirms directory evidence.
func removeResource(source, root, path string, directory bool) error {
	repo := preview.OrphanTracking()
	if repo == nil {
		return os.RemoveAll(path)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if directory {
		inventory, err := diskcache.ObserveSourceDirectory(ctx, root, path, 32)
		if err != nil {
			return os.RemoveAll(path)
		}
		mutation := preview.BeginOrphanDirectory(ctx, repo, source, root, inventory)
		cancel()
		err = os.RemoveAll(path)
		mutation.Finish(err == nil)
		return err
	}
	unlock, err := repo.LockLocations(ctx)
	if err != nil {
		return os.RemoveAll(path)
	}
	mutation := preview.BeginOrphanMutation(ctx, repo, source, root, path, "delete")
	if mutation == nil {
		unlock()
		return os.RemoveAll(path)
	}
	// A stale file type or a concurrent replacement must never put a
	// recursive deletion under the location gate.
	err = os.Remove(path)
	mutation.Finish(ctx, err == nil && mutation.ProvesUnlinked(false), "")
	unlock()
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return os.RemoveAll(path)
}

func openResourceWriter(source, root, path string) (*os.File, *preview.OrphanMutation, error) {
	open := func() (*os.File, *preview.OrphanMutation, error) {
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, fileutils.PermFile)
		return f, nil, err
	}
	repo := preview.OrphanTracking()
	if repo == nil {
		return open()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	unlock, err := repo.LockLocations(ctx)
	if err != nil {
		return open()
	}
	defer unlock()
	m := preview.BeginOrphanMutation(ctx, repo, source, root, path, "overwrite")
	if m == nil {
		return open()
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, fileutils.PermFile)
	if err != nil {
		m.Finish(ctx, false, "")
		return nil, nil, err
	}
	info, statErr := f.Stat()
	if statErr != nil || !m.MatchesOld(info) {
		m.Finish(ctx, false, "")
		m = nil
	}
	if err := f.Truncate(0); err != nil {
		_ = f.Close()
		m.Finish(ctx, false, "")
		return nil, nil, err
	}
	return f, m, nil
}
