//go:build linux && !386 && !arm

package previewwarmup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"golang.org/x/sys/unix"
)

const platformSupported = true

type directoryID struct {
	deviceMajor, deviceMinor uint32
	inode, mount             uint64
}

func (d directoryID) sameDirectory(other directoryID) bool {
	return d.deviceMajor == other.deviceMajor && d.deviceMinor == other.deviceMinor && d.inode == other.inode
}

type scopeID struct {
	source, directory, mount directoryID
}

type guardedFile struct {
	scope           *scope
	relative        string
	version         string
	claimedIdentity scopeID
}

func statDirectory(fd int) (directoryID, error) {
	var st unix.Statx_t
	for _, idMask := range []int{unix.STATX_MNT_ID_UNIQUE, unix.STATX_MNT_ID} {
		mask := unix.STATX_INO | idMask
		if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH, mask, &st); err == nil && st.Mask&uint32(mask) == uint32(mask) {
			return directoryID{st.Dev_major, st.Dev_minor, st.Ino, st.Mnt_id}, nil
		}
	}
	return directoryID{}, errScopeOffline
}

func openDirectory(name string) (int, directoryID, error) {
	fd, err := unix.Openat2(unix.AT_FDCWD, name, &unix.OpenHow{
		Flags: unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS,
	})
	if err != nil {
		return -1, directoryID{}, errScopeOffline
	}
	id, err := statDirectory(fd)
	if err != nil {
		unix.Close(fd)
		return -1, directoryID{}, err
	}
	return fd, id, nil
}

func (s *scope) openScope() (int, scopeID, error) {
	if !s.indexReady() {
		return -1, scopeID{}, errIndexNotReady
	}
	source, sourceID, err := openDirectory(s.sourceRoot)
	if err != nil {
		return -1, scopeID{}, err
	}
	defer unix.Close(source)

	// Anchor the traversal in the current source descriptor. A configured
	// mount point must really be a mount, even on the first startup.
	mountFD, mountID := source, sourceID
	belowMount := s.config.Directory
	if s.config.MountPoint != "" {
		mountPath := filepath.Join(s.sourceRoot, s.config.MountPoint)
		parent, parentID, err := openDirectory(filepath.Dir(mountPath))
		if err != nil {
			return -1, scopeID{}, err
		}
		unix.Close(parent)
		if s.config.MountPoint != "." {
			mountFD, err = openBelow(source, s.config.MountPoint, false, true)
			if err != nil {
				return -1, scopeID{}, errScopeOffline
			}
			defer unix.Close(mountFD)
			mountID, err = statDirectory(mountFD)
			if err != nil {
				return -1, scopeID{}, err
			}
		}
		if mountID.mount == parentID.mount {
			return -1, scopeID{}, errScopeOffline
		}
		belowMount, err = filepath.Rel(s.config.MountPoint, s.config.Directory)
		if err != nil {
			return -1, scopeID{}, errScopeOffline
		}
	}

	fd, err := openBelow(mountFD, belowMount, true, true)
	if err != nil {
		return -1, scopeID{}, errScopeOffline
	}
	directoryID, err := statDirectory(fd)
	id := scopeID{sourceID, directoryID, mountID}
	if err != nil || (s.pinned != (scopeID{}) &&
		(!s.pinned.source.sameDirectory(id.source) || !s.pinned.directory.sameDirectory(id.directory) ||
			!s.pinned.mount.sameDirectory(id.mount))) {
		unix.Close(fd)
		return -1, scopeID{}, errScopeOffline
	}
	return fd, id, nil
}

func openBelow(parent int, relative string, sameMount, directory bool) (int, error) {
	how := unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS,
	}
	if sameMount {
		how.Resolve |= unix.RESOLVE_NO_XDEV
	}
	if directory {
		how.Flags |= unix.O_DIRECTORY
	}
	return unix.Openat2(parent, relative, &how)
}

func (s *scope) online() (scopeID, error) {
	fd, id, err := s.openScope()
	if err == nil {
		unix.Close(fd)
		s.pinned = id
	}
	return id, err
}

func (g guardedFile) descriptor(ctx context.Context) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, id, err := g.scope.openScope()
	if err != nil {
		return nil, err
	}
	defer unix.Close(root)
	if id != g.claimedIdentity {
		return nil, errScopeOffline
	}
	fd, err := openBelow(root, g.relative, true, false)
	if err != nil {
		switch {
		case errors.Is(err, unix.ENOENT), errors.Is(err, unix.ENOTDIR),
			errors.Is(err, unix.ELOOP), errors.Is(err, unix.EXDEV):
			return nil, preview.ErrPreviewSourceChanged
		case errors.Is(err, unix.ENODEV), errors.Is(err, unix.ESTALE), errors.Is(err, unix.EIO):
			return nil, errScopeOffline
		default:
			return nil, errFileUnavailable
		}
	}
	f := os.NewFile(uintptr(fd), "warmup-source")
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !indexing.MatchesPreviewWarmupVersion(info, g.version) {
		f.Close()
		return nil, preview.ErrPreviewSourceChanged
	}
	return f, nil
}

func (g guardedFile) stat(ctx context.Context) (os.FileInfo, error) {
	f, err := g.descriptor(ctx)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Stat()
}

func (g guardedFile) open(ctx context.Context) (*os.File, error) {
	ref, err := g.descriptor(ctx)
	if err != nil {
		return nil, err
	}
	defer ref.Close()
	// O_PATH validates the regular file before opening it for reading. The
	// kernel's own descriptor link opens that inode, even after a rename.
	file, err := os.Open(fmt.Sprintf("/proc/self/fd/%d", ref.Fd()))
	if err != nil {
		return nil, errFileUnavailable
	}
	info, err := file.Stat()
	if err != nil || !indexing.MatchesPreviewWarmupVersion(info, g.version) {
		file.Close()
		return nil, preview.ErrPreviewSourceChanged
	}
	return file, nil
}

const memoryReserve = 96 << 20

type resourceProbe struct {
	cache     string
	minFree   uint64
	cgroups   []string
	throttled map[string]uint64
	maxDecode int64
}

func newResourceProbe(cache string) resourceProbe {
	root := "/sys/fs/cgroup"
	var samples []string
	if data, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if name, ok := strings.CutPrefix(line, "0::"); ok && filepath.IsAbs(name) {
				for dir := filepath.Join(root, name); dir != root && strings.HasPrefix(dir, root+"/"); dir = filepath.Dir(dir) {
					samples = append(samples, dir)
				}
				break
			}
		}
	}
	return resourceProbe{cache: filepath.Join(cache, "diskcache"), minFree: 512 << 20,
		cgroups: append(samples, root), throttled: make(map[string]uint64)}
}

func (p *resourceProbe) poll() error {
	var filesystem unix.Statfs_t
	if err := unix.Statfs(p.cache, &filesystem); err != nil {
		return errPressureUnavailable
	}
	if filesystem.Flags&unix.ST_RDONLY != 0 || filesystem.Bavail*uint64(filesystem.Bsize) < p.minFree ||
		(filesystem.Files > 0 && filesystem.Ffree < 32) {
		return errLowSpace
	}
	p.maxDecode = 1<<63 - 1
	haveCPU, haveLimit, throttled := false, false, false
	for _, dir := range p.cgroups {
		cpu, ok := readCounter(filepath.Join(dir, "cpu.stat"), "nr_throttled")
		if ok {
			haveCPU = true
			previous, sampled := p.throttled[dir]
			throttled = throttled || (sampled && cpu > previous)
			p.throttled[dir] = cpu
		} else if _, ok := readCounter(filepath.Join(dir, "cpu.stat"), "usage_usec"); ok {
			if quota, err := os.ReadFile(filepath.Join(dir, "cpu.max")); err == nil && !strings.HasPrefix(string(quota), "max ") {
				return errPressureUnavailable
			}
			haveCPU = true
		}
		limit, err := os.ReadFile(filepath.Join(dir, "memory.max"))
		if err != nil || strings.TrimSpace(string(limit)) == "max" {
			continue
		}
		maxBytes, err := strconv.ParseInt(strings.TrimSpace(string(limit)), 10, 64)
		current, ok := readCounter(filepath.Join(dir, "memory.current"), "")
		if err != nil || !ok {
			return errPressureUnavailable
		}
		haveLimit = true
		p.maxDecode = min(p.maxDecode, maxBytes-int64(current)-memoryReserve)
	}
	if !haveCPU {
		return errPressureUnavailable
	}
	if throttled {
		return errCPUThrottled
	}
	if !haveLimit {
		kb, ok := readCounter("/proc/meminfo", "MemAvailable:")
		if !ok {
			return errPressureUnavailable
		}
		p.maxDecode = min(96<<20, int64(kb)*1024-(512<<20))
	}
	return p.check(24 << 20)
}

func (p *resourceProbe) check(estimate int64) error {
	if estimate > p.maxDecode {
		return errMemoryPressure
	}
	return nil
}

func readCounter(path, key string) (uint64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	if key == "" {
		value, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
		return value, err == nil
	}
	for _, line := range strings.Split(string(data), "\n") {
		name, value, ok := strings.Cut(line, " ")
		if ok && name == key {
			fields := strings.Fields(value)
			if len(fields) > 0 {
				value, err := strconv.ParseUint(fields[0], 10, 64)
				return value, err == nil
			}
		}
	}
	return 0, false
}
