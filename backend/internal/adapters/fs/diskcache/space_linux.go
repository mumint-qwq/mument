//go:build linux

package diskcache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/fileutils"
	"golang.org/x/sys/unix"
)

func (t *ReclaimTree) SpaceSample() (SpaceSample, error) {
	if err := t.Check(false); err != nil {
		return SpaceSample{}, err
	}
	if !t.HasDurableRoot() {
		return SpaceSample{}, ErrDurableIdentity
	}
	var fs unix.Statfs_t
	if err := unix.Fstatfs(t.fd, &fs); err != nil {
		return SpaceSample{}, err
	}
	unit := int64(fs.Frsize)
	if unit <= 0 {
		unit = int64(fs.Bsize)
	}
	if unit <= 0 || fs.Bavail > uint64(math.MaxInt64/unit) {
		return SpaceSample{}, ErrReclaimScope
	}
	return SpaceSample{
		Available: int64(fs.Bavail) * unit, Block: unit, Inodes: fs.Ffree, HasInodes: fs.Files > 0,
		ReadOnly: fs.Flags&unix.ST_RDONLY != 0, Mount: t.identity.Mount, Root: t.identity.DirectoryIdentity,
	}, nil
}

type inventoryDirectory struct {
	fd    *os.File
	start unix.Stat_t
}
type inventoryInode struct{ dev, ino uint64 }

// Inventory holds a fixed-depth stack and only retains inode identities for
// multiply linked files. The caller commits totals only after a full pass.
type Inventory struct {
	stack []inventoryDirectory
	links map[inventoryInode]struct{}
	total Usage
}

func (t *ReclaimTree) NewInventory() (*Inventory, error) {
	if _, err := t.SpaceSample(); err != nil {
		return nil, err
	}
	w := &Inventory{links: make(map[inventoryInode]struct{})}
	if err := w.push(t.fd, "."); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Inventory) push(parent int, name string) error {
	fd, err := openWithin(parent, name, safeDir, 0)
	if err != nil {
		return err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return err
	}
	w.stack = append(w.stack, inventoryDirectory{os.NewFile(uintptr(fd), "cache inventory"), st})
	return nil
}

func (w *Inventory) Close() {
	for _, d := range w.stack {
		d.fd.Close()
	}
	w.stack = nil
}

func (w *Inventory) Step(ctx context.Context, entries int) (Usage, bool, error) {
	for ; entries > 0 && len(w.stack) > 0; entries-- {
		if err := ctx.Err(); err != nil {
			return w.total, false, err
		}
		d := w.stack[len(w.stack)-1]
		fd := int(d.fd.Fd())
		names, err := d.fd.Readdirnames(1)
		if errors.Is(err, io.EOF) && len(names) == 0 {
			var now unix.Stat_t
			if err := unix.Fstat(fd, &now); err != nil {
				return w.total, false, err
			}
			if d.start.Mtim != now.Mtim || d.start.Ctim != now.Ctim {
				return w.total, false, ErrSpaceChanged
			}
			d.fd.Close()
			w.stack = w.stack[:len(w.stack)-1]
			continue
		}
		if err != nil {
			return w.total, false, err
		}
		if err := w.observe(fd, names[0]); err != nil {
			return w.total, false, err
		}
	}
	return w.total, len(w.stack) == 0, nil
}

func (w *Inventory) observe(parent int, name string) error {
	fd, err := openWithin(parent, name, unix.O_PATH, 0)
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EXDEV) {
		w.total.Excluded++
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		if len(w.stack) >= 12 {
			return ErrReclaimScope
		}
		return w.push(parent, name)
	case unix.S_IFREG:
		if st.Nlink > 1 {
			key := inventoryInode{uint64(st.Dev), st.Ino}
			if _, seen := w.links[key]; seen {
				return nil
			}
			if len(w.links) >= 65536 {
				return ErrReclaimScope
			}
			w.links[key] = struct{}{}
		}
		if st.Size < 0 || st.Blocks < 0 || st.Size > math.MaxInt64-w.total.Logical || st.Blocks > (math.MaxInt64-w.total.Allocated)/512 {
			return ErrReclaimScope
		}
		w.total.Logical += st.Size
		w.total.Allocated += st.Blocks * 512
		w.total.Files++
	default:
		w.total.Excluded++
	}
	return nil
}

// Store reserves metadata at the caller before obtaining any destination
// directory or temporary file. Every write stays under the leased descriptor.
func (t *ReclaimTree) Store(ctx context.Context, key string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := t.Check(true); err != nil {
		return err
	}
	path := RelativePath(key)
	parent := t.fd
	defer func() {
		if parent != t.fd {
			unix.Close(parent)
		}
	}()
	for _, name := range strings.Split(filepath.Dir(path), "/") {
		if err := unix.Mkdirat(parent, name, uint32(fileutils.PermDir.Perm())); err != nil && !errors.Is(err, unix.EEXIST) {
			return err
		}
		next, err := openWithin(parent, name, safeDir, 0)
		if err != nil {
			return err
		}
		if parent != t.fd {
			unix.Close(parent)
		}
		parent = next
		if err := privateDirectory(parent); err != nil {
			return err
		}
	}
	leaf := filepath.Base(path)
	var previous unix.Stat_t
	if err := unix.Fstatat(parent, leaf, &previous, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		if previous.Mode&unix.S_IFMT != unix.S_IFREG || previous.Nlink != 1 {
			return ErrReclaimLinks
		}
	} else if !errors.Is(err, unix.ENOENT) {
		return err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	name := ".preview-" + hex.EncodeToString(random[:])
	fd, err := openWithin(parent, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(parent, name, 0)
	if err := writeCacheValue(ctx, os.NewFile(uintptr(fd), "cache write"), value); err != nil {
		return err
	}
	obj := &CacheObject{tree: t, dir: parent, parent: filepath.Dir(path), leaf: leaf}
	if err := t.Check(true); err != nil {
		return err
	}
	if err := obj.current(); err != nil {
		return err
	}
	var now unix.Stat_t
	flags := uint(0)
	err = unix.Fstatat(parent, leaf, &now, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) && previous.Ino == 0 {
		flags = unix.RENAME_NOREPLACE
	} else if err != nil || now.Dev != previous.Dev || now.Ino != previous.Ino || now.Ctim != previous.Ctim || now.Nlink != 1 {
		return ErrReclaimIdentity
	}
	return unix.Renameat2(parent, name, parent, leaf, flags)
}

func SpaceWriteReason(err error) string {
	switch {
	case errors.Is(err, unix.ENOSPC):
		return "no_space"
	case errors.Is(err, unix.EDQUOT):
		return "quota"
	case errors.Is(err, unix.EROFS):
		return "read_only"
	default:
		return ""
	}
}
