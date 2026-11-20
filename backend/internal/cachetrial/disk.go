//go:build linux

package cachetrial

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const noSubmount = unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV | unix.RESOLVE_BENEATH
const directoryFlags = unix.O_DIRECTORY | unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOATIME

func openDisk(parent string) (int, error) {
	root, err := filepath.Abs(filepath.Join(parent, "diskcache"))
	if err != nil {
		return -1, err
	}
	return unix.Openat2(unix.AT_FDCWD, root, &unix.OpenHow{
		Flags: directoryFlags, Resolve: unix.RESOLVE_NO_SYMLINKS,
	})
}

func fileInfo(root int, relative string) (unix.Stat_t, string) {
	var stat unix.Stat_t
	fd, err := unix.Openat2(root, relative, &unix.OpenHow{
		Flags: unix.O_PATH | unix.O_NOFOLLOW | unix.O_CLOEXEC, Resolve: noSubmount,
	})
	if err != nil {
		return stat, diskReason(err)
	}
	defer unix.Close(fd)
	if err := unix.Fstat(fd, &stat); err != nil {
		return stat, diskReason(err)
	}
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		return stat, ""
	case unix.S_IFLNK:
		return stat, "symlink"
	default:
		return stat, "not_regular"
	}
}

func diskReason(err error) string {
	switch {
	case errors.Is(err, unix.ENOENT):
		return "missing"
	case errors.Is(err, unix.EACCES), errors.Is(err, unix.EPERM):
		return "permission"
	case errors.Is(err, unix.ELOOP):
		return "symlink"
	case errors.Is(err, unix.EXDEV):
		return "mount_boundary"
	case errors.Is(err, unix.ENOTDIR):
		return "not_directory"
	default:
		return "stat_or_read_failed"
	}
}

type dirStamp struct {
	Dev, Ino          uint64
	Modified, Changed unix.Timespec
}

func stamp(fd int) (dirStamp, error) {
	var stat unix.Stat_t
	err := unix.Fstat(fd, &stat)
	return dirStamp{uint64(stat.Dev), stat.Ino, stat.Mtim, stat.Ctim}, err
}

type position struct {
	Shard  string
	Cookie int64
	Stamp  dirStamp
}

type frame struct {
	position
	fd          int
	buffer      [8192]byte
	offset, end int
}

type walker struct {
	root  int
	stack []*frame
}

func newWalker(root int, cursor string) (*walker, error) {
	w := &walker{root: root}
	if cursor == "done" {
		return w, nil
	}
	if cursor == "" {
		if err := w.push(position{}); err != nil {
			return nil, err
		}
		return w, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	var positions []position
	if err != nil || json.Unmarshal(raw, &positions) != nil || len(positions) < 1 || len(positions) > 3 {
		return nil, errors.New("invalid cache cursor")
	}
	for depth, p := range positions {
		if (depth == 0 && p.Shard != "") || (depth > 0 && !lowerHex(p.Shard, depth)) {
			w.close()
			return nil, errors.New("invalid cache cursor shard")
		}
		if err := w.push(p); err != nil {
			w.close()
			return nil, errors.New("cache cursor no longer matches directory; restart inventory")
		}
	}
	return w, nil
}

func (w *walker) push(p position) error {
	parent, name := w.root, "."
	if len(w.stack) > 0 {
		parent, name = w.stack[len(w.stack)-1].fd, p.Shard
	}
	fd, err := unix.Openat2(parent, name, &unix.OpenHow{Flags: directoryFlags, Resolve: noSubmount})
	if err != nil {
		return err
	}
	now, err := stamp(fd)
	if err == nil && p.Stamp != (dirStamp{}) && p.Stamp != now {
		err = errors.New("directory changed")
	}
	if err == nil && p.Cookie != 0 {
		_, err = unix.Seek(fd, p.Cookie, io.SeekStart)
	}
	if err != nil {
		unix.Close(fd)
		return err
	}
	p.Stamp = now
	w.stack = append(w.stack, &frame{position: p, fd: fd})
	return nil
}

func (w *walker) pop() {
	top := w.stack[len(w.stack)-1]
	unix.Close(top.fd)
	w.stack = w.stack[:len(w.stack)-1]
}

func (w *walker) close() {
	for len(w.stack) > 0 {
		w.pop()
	}
}

func (w *walker) cursor() string {
	if len(w.stack) == 0 {
		return ""
	}
	positions := make([]position, 0, 3)
	for _, f := range w.stack {
		positions = append(positions, f.position)
	}
	raw, _ := json.Marshal(positions)
	return base64.RawURLEncoding.EncodeToString(raw)
}

type diskEntry struct {
	relative, kind, reason string
	stat                   unix.Stat_t
}

func (w *walker) next(ctx context.Context) (diskEntry, error) {
	for len(w.stack) > 0 {
		if err := ctx.Err(); err != nil {
			return diskEntry{}, err
		}
		f := w.stack[len(w.stack)-1]
		name, err := f.nextName()
		if err != nil {
			current, stampErr := stamp(f.fd)
			w.pop()
			switch {
			case err != io.EOF:
				return diskEntry{reason: diskReason(err)}, nil
			case stampErr != nil || current != f.Stamp:
				return diskEntry{reason: "directory_changed"}, nil
			default:
				continue
			}
		}
		if name == "." || name == ".." {
			continue
		}
		depth := len(w.stack)
		if depth < 3 {
			if !lowerHex(name, depth) {
				return diskEntry{reason: "outside_hash_layout"}, nil
			}
			if err := w.push(position{Shard: name}); err != nil {
				return diskEntry{reason: diskReason(err)}, nil
			}
			return diskEntry{kind: "shard"}, nil
		}
		first, second := w.stack[1].Shard, w.stack[2].Shard
		kind := "hash"
		if strings.HasPrefix(name, ".preview-") {
			kind = "temporary"
		} else if !lowerHex(name, 40) || name[:3] != first+second {
			return diskEntry{reason: "outside_hash_layout"}, nil
		}
		relative := filepath.Join(first, second, name)
		stat, reason := fileInfo(w.root, relative)
		return diskEntry{relative: relative, kind: kind, reason: reason, stat: stat}, nil
	}
	return diskEntry{}, io.EOF
}

func lowerHex(s string, length int) bool {
	if len(s) != length {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Linux getdents64 supplies an opaque cookie for the next record. Holding a
// fixed buffer permits resuming inside a shard without sorting or rereading it.
func (f *frame) nextName() (string, error) {
	if f.offset == f.end {
		n, err := unix.Getdents(f.fd, f.buffer[:])
		if err != nil {
			return "", err
		}
		if n == 0 {
			return "", io.EOF
		}
		f.offset, f.end = 0, n
	}
	const nameOffset = 19
	record := f.buffer[f.offset:f.end]
	if len(record) <= nameOffset {
		return "", errors.New("short directory entry")
	}
	length := int(binary.NativeEndian.Uint16(record[16:18]))
	if length <= nameOffset || length > len(record) {
		return "", errors.New("invalid directory entry size")
	}
	f.Cookie = int64(binary.NativeEndian.Uint64(record[8:16]))
	f.offset += length
	name, _, _ := strings.Cut(string(record[nameOffset:length]), "\x00")
	return name, nil
}
