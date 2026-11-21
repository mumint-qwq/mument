//go:build linux

package diskcache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const withinCache = unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV | unix.RESOLVE_BENEATH
const safeDir = unix.O_DIRECTORY | unix.O_RDONLY | unix.O_CLOEXEC
const isolateDirectory = ".orphan-reclamation"

type ReclaimTree struct {
	fd       int
	root     string
	identity mountedDirectory
}

func openDirectoryPath(name string) (int, error) {
	return unix.Openat2(unix.AT_FDCWD, name, &unix.OpenHow{Flags: safeDir, Resolve: unix.RESOLVE_NO_SYMLINKS})
}

// The mount ID belongs to the current namespace. Device and fsid identify
// the volume, while inode and birth time distinguish a directory on it.
type mountedDirectory struct {
	DirectoryIdentity
	Mount uint64
}

func directoryIdentity(fd int) (mountedDirectory, error) {
	var st unix.Statx_t
	var fs unix.Statfs_t
	for _, mount := range []int{unix.STATX_MNT_ID_UNIQUE, unix.STATX_MNT_ID} {
		mask := unix.STATX_INO | mount
		if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH, mask|unix.STATX_BTIME, &st); err != nil || int(st.Mask)&mask != mask || st.Mnt_id == 0 {
			continue
		}
		if err := unix.Fstatfs(fd, &fs); err != nil {
			return mountedDirectory{}, err
		}
		return mountedDirectory{DirectoryIdentity{
			Dev: unix.Mkdev(st.Dev_major, st.Dev_minor), Ino: st.Ino,
			Birth: birthTime(st), Filesystem: durableFilesystem(fs),
		}, st.Mnt_id}, nil
	}
	return mountedDirectory{}, ErrReclaimScope
}

func birthTime(st unix.Statx_t) int64 {
	if st.Mask&unix.STATX_BTIME == 0 {
		return 0
	}
	return st.Btime.Sec*1e9 + int64(st.Btime.Nsec)
}

func durableFilesystem(fs unix.Statfs_t) string {
	if fs.Fsid.Val == [2]int32{} {
		return ""
	}
	switch uint32(fs.Type) {
	case unix.EXT4_SUPER_MAGIC, unix.XFS_SUPER_MAGIC, unix.BTRFS_SUPER_MAGIC, unix.TMPFS_MAGIC:
		return fmt.Sprintf("%x:%v", fs.Type, fs.Fsid.Val)
	default:
		return ""
	}
}

func openWithin(fd int, name string, flags uint64, mode uint64) (int, error) {
	return unix.Openat2(fd, name, &unix.OpenHow{Flags: flags | unix.O_CLOEXEC, Mode: mode, Resolve: withinCache})
}

// OpenReclaimTree holds an advisory lease on the directory inode. Alias
// paths contend for the same lease, while execution separately rejects aliases.
func OpenReclaimTree(cache string) (*ReclaimTree, error) {
	root, err := filepath.Abs(filepath.Join(cache, "diskcache"))
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open(root, safeDir, 0)
	if err != nil {
		return nil, err
	}
	id, err := directoryIdentity(fd)
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		unix.Close(fd)
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errors.Join(ErrReclaimBusy, err)
		}
		return nil, err
	}
	return &ReclaimTree{fd: fd, root: root, identity: id}, nil
}

func (t *ReclaimTree) Close() error { return unix.Close(t.fd) }

func privateDirectory(fd int) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Uid != uint32(os.Geteuid()) || st.Mode&0o022 != 0 {
		return ErrReclaimScope
	}
	return nil
}

func (t *ReclaimTree) Check(writable bool) error {
	fd, err := openDirectoryPath(t.root)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	id, err := directoryIdentity(fd)
	if err != nil || id != t.identity {
		return ErrReclaimIdentity
	}
	if err := privateDirectory(fd); err != nil {
		return err
	}
	var fs unix.Statfs_t
	if err = unix.Fstatfs(fd, &fs); err != nil {
		return err
	}
	if writable && fs.Flags&unix.ST_RDONLY != 0 {
		return unix.EROFS
	}
	return nil
}

func (t *ReclaimTree) object(fd int, leaf string) (ObjectStamp, error) {
	object, err := openWithin(fd, leaf, unix.O_PATH, 0)
	if err != nil {
		return ObjectStamp{}, err
	}
	defer unix.Close(object)
	return t.stamp(object)
}

func (t *ReclaimTree) HasDurableRoot() bool { return t.identity.valid() }

func (t *ReclaimTree) CheckRecordedRoot(expected DirectoryIdentity) error {
	if !expected.valid() || !t.HasDurableRoot() {
		return ErrDurableIdentity
	}
	if t.identity.DirectoryIdentity != expected {
		return ErrReclaimIdentity
	}
	return t.Check(false)
}

func (t *ReclaimTree) stamp(fd int) (ObjectStamp, error) {
	var st unix.Statx_t
	const basic = unix.STATX_TYPE | unix.STATX_NLINK | unix.STATX_INO | unix.STATX_SIZE |
		unix.STATX_MTIME | unix.STATX_CTIME | unix.STATX_BLOCKS
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH, basic|unix.STATX_BTIME, &st); err != nil {
		return ObjectStamp{}, err
	}
	if st.Mask&basic != basic {
		return ObjectStamp{}, ErrReclaimScope
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 {
		return ObjectStamp{}, ErrReclaimLinks
	}
	stamp := ObjectStamp{
		Root: t.identity.DirectoryIdentity, Dev: unix.Mkdev(st.Dev_major, st.Dev_minor), Ino: st.Ino,
		Birth: birthTime(st), Size: int64(st.Size), Mtime: st.Mtime.Sec*1e9 + int64(st.Mtime.Nsec),
		Ctime: st.Ctime.Sec*1e9 + int64(st.Ctime.Nsec), Blocks: int64(st.Blocks) * 512,
	}
	if !stamp.HasDurableIdentity() {
		return ObjectStamp{}, ErrDurableIdentity
	}
	return stamp, nil
}

func (t *ReclaimTree) OpenKey(key string) (*CacheObject, error) {
	path := RelativePath(key)
	return t.openObject(filepath.Dir(path), filepath.Base(path), false)
}

func (t *ReclaimTree) OpenIsolated(id string, create bool) (*CacheObject, error) {
	if id == "" || len(id) > 64 || strings.IndexFunc(id, func(r rune) bool {
		return !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) >= 0 {
		return nil, ErrReclaimScope
	}
	return t.openObject(isolateDirectory, id, create)
}

func (t *ReclaimTree) openObject(parent, leaf string, create bool) (*CacheObject, error) {
	if create {
		if err := unix.Mkdirat(t.fd, parent, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, err
		}
		if err := unix.Fsync(t.fd); err != nil {
			return nil, err
		}
	}
	dir, err := t.openCacheDirectory(parent)
	if err != nil {
		return nil, err
	}
	return &CacheObject{tree: t, dir: dir, parent: parent, leaf: leaf}, nil
}

func (t *ReclaimTree) openCacheDirectory(path string) (int, error) {
	if err := privateDirectory(t.fd); err != nil {
		return -1, err
	}
	parent := t.fd
	for _, name := range strings.Split(path, "/") {
		child, err := openWithin(parent, name, safeDir, 0)
		if parent != t.fd {
			unix.Close(parent)
		}
		if err != nil {
			return -1, err
		}
		if err := privateDirectory(child); err != nil {
			unix.Close(child)
			return -1, err
		}
		parent = child
	}
	return parent, nil
}

type CacheObject struct {
	tree         *ReclaimTree
	dir          int
	parent, leaf string
}

func (o *CacheObject) Close() error                { return unix.Close(o.dir) }
func (o *CacheObject) Stamp() (ObjectStamp, error) { return o.tree.object(o.dir, o.leaf) }

func (o *CacheObject) current() error {
	dir, err := o.tree.openCacheDirectory(o.parent)
	if err != nil {
		return err
	}
	defer unix.Close(dir)
	var a, b unix.Stat_t
	if err := unix.Fstat(dir, &a); err != nil {
		return err
	}
	if err := unix.Fstat(o.dir, &b); err != nil {
		return err
	}
	if a.Dev != b.Dev || a.Ino != b.Ino || a.Uid != uint32(os.Geteuid()) || a.Mode&0o022 != 0 {
		return ErrReclaimIdentity
	}
	return nil
}

// Check validates the root, parent directory and the current leaf identity.
func (o *CacheObject) Check(expected ObjectStamp) error {
	if err := o.tree.CheckRecordedRoot(expected.Root); err != nil {
		return err
	}
	if err := o.current(); err != nil {
		return err
	}
	current, err := o.Stamp()
	if err != nil {
		return err
	}
	if current != expected {
		return ErrReclaimIdentity
	}
	return nil
}

func (o *CacheObject) Verify(ctx context.Context, before ObjectStamp, digest string) error {
	if err := o.tree.CheckRecordedRoot(before.Root); err != nil {
		return err
	}
	if err := o.current(); err != nil {
		return err
	}
	fd, err := openWithin(o.dir, o.leaf, unix.O_PATH, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	now, err := o.tree.stamp(fd)
	if err != nil {
		return err
	}
	if now != before {
		return ErrReclaimIdentity
	}
	file, err := os.OpenFile("/proc/self/fd/"+strconv.Itoa(fd), os.O_RDONLY|unix.O_NOATIME, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	sum := sha256.New()
	buf := make([]byte, 32<<10)
	for remaining := before.Size; remaining > 0; {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := io.ReadFull(file, buf[:min(int64(len(buf)), remaining)])
		if err != nil {
			return err
		}
		sum.Write(buf[:n])
		remaining -= int64(n)
	}
	now, err = o.Stamp()
	if err != nil {
		return err
	}
	if now != before || hex.EncodeToString(sum.Sum(nil)) != digest {
		return ErrReclaimIdentity
	}
	return o.Check(before)
}

func (o *CacheObject) Move(to *CacheObject, expected ObjectStamp) error {
	if err := o.tree.Check(true); err != nil {
		return err
	}
	if err := o.current(); err != nil {
		return err
	}
	if err := to.current(); err != nil {
		return err
	}
	before, err := o.Stamp()
	if err != nil {
		return err
	}
	if before != expected {
		return ErrReclaimIdentity
	}
	if err := unix.Renameat2(o.dir, o.leaf, to.dir, to.leaf, unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	return errors.Join(unix.Fsync(o.dir), unix.Fsync(to.dir))
}

func (o *CacheObject) Unlink(expected ObjectStamp) error {
	if err := o.tree.Check(true); err != nil {
		return err
	}
	if err := o.current(); err != nil {
		return err
	}
	before, err := o.Stamp()
	if err != nil {
		return err
	}
	if before != expected {
		return ErrReclaimIdentity
	}
	if err = unix.Unlinkat(o.dir, o.leaf, 0); err != nil {
		return err
	}
	return unix.Fsync(o.dir)
}

// openParent binds the recorded directories to this namespace's live root
// and to a parent reached without crossing any mount or symlink.
func (p SourceProof) openParent() (int, error) {
	root, err := openDirectoryPath(p.Root)
	if err != nil {
		return -1, err
	}
	defer unix.Close(root)
	rootID, err := directoryIdentity(root)
	if err != nil {
		return -1, err
	}
	if rootID.DirectoryIdentity != p.RootID || (p.mount != 0 && p.mount != rootID.Mount) {
		return -1, ErrReclaimIdentity
	}
	rel, err := filepath.Rel(p.Root, p.Parent)
	if err != nil || !filepath.IsLocal(rel) {
		return -1, ErrReclaimScope
	}
	parent, err := openWithin(root, rel, safeDir, 0)
	if err != nil {
		return -1, err
	}
	parentID, err := directoryIdentity(parent)
	if err != nil {
		unix.Close(parent)
		return -1, err
	}
	if parentID.Mount != rootID.Mount || (p.ParentID != (DirectoryIdentity{}) && parentID.DirectoryIdentity != p.ParentID) {
		unix.Close(parent)
		return -1, ErrReclaimIdentity
	}
	return parent, nil
}

func (p SourceProof) Check() error {
	if !p.RootID.valid() || !p.ParentID.valid() {
		return ErrDurableIdentity
	}
	fd, err := p.openParent()
	if err == nil {
		unix.Close(fd)
	}
	return err
}

func observeSourceParent(root, path string) (SourceProof, int, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return SourceProof{}, -1, err
	}
	fd, err := openDirectoryPath(absolute)
	if err != nil {
		return SourceProof{}, -1, err
	}
	defer unix.Close(fd)
	id, err := directoryIdentity(fd)
	if err != nil {
		return SourceProof{}, -1, err
	}
	proof := SourceProof{Root: absolute, Parent: filepath.Dir(path), RootID: id.DirectoryIdentity, mount: id.Mount}
	parent, err := proof.openParent()
	if err != nil {
		return SourceProof{}, -1, err
	}
	parentID, err := directoryIdentity(parent)
	if err != nil {
		unix.Close(parent)
		return SourceProof{}, -1, err
	}
	proof.ParentID = parentID.DirectoryIdentity
	return proof, parent, nil
}

func ObserveSource(root, path string) (*SourceObservation, error) {
	p, parent, err := observeSourceParent(root, path)
	if err != nil {
		return nil, err
	}
	defer unix.Close(parent)
	fd, err := openWithin(parent, filepath.Base(path), unix.O_PATH, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "source")
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, ErrReclaimScope
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		file.Close()
		return nil, err
	}
	return &SourceObservation{Info: info, Proof: p, file: file, links: uint64(st.Nlink), path: filepath.Base(path)}, nil
}

// Unlinked verifies the captured inode lost a link and the original name is no
// longer that inode. Missing original and replacement are distinct proofs.
func (o *SourceObservation) Unlinked(replaced bool) bool {
	parent, err := o.Proof.openParent()
	if err != nil {
		return false
	}
	defer unix.Close(parent)
	var old unix.Stat_t
	if !o.LostLink() || unix.Fstat(int(o.file.Fd()), &old) != nil {
		return false
	}
	fd, err := openWithin(parent, o.path, unix.O_PATH, 0)
	if !replaced {
		if err == nil {
			unix.Close(fd)
		}
		return errors.Is(err, unix.ENOENT)
	}
	if err != nil {
		return false
	}
	defer unix.Close(fd)
	var now unix.Stat_t
	return unix.Fstat(fd, &now) == nil && now.Mode&unix.S_IFMT == unix.S_IFREG && (now.Dev != old.Dev || now.Ino != old.Ino)
}

func (o *SourceObservation) Updated(written os.FileInfo) bool {
	parent, err := o.Proof.openParent()
	if err != nil {
		return false
	}
	defer unix.Close(parent)
	fd, err := openWithin(parent, o.path, unix.O_PATH, 0)
	if err != nil {
		return false
	}
	file := os.NewFile(uintptr(fd), "source")
	defer file.Close()
	info, err := file.Stat()
	return err == nil && os.SameFile(o.Info, written) && os.SameFile(info, written)
}

func ReclaimReason(err error) string {
	switch {
	case errors.Is(err, unix.ENOENT):
		return "missing"
	case errors.Is(err, unix.ELOOP):
		return "symlink"
	case errors.Is(err, unix.EXDEV):
		return "mount_boundary"
	case errors.Is(err, unix.EROFS):
		return "read_only"
	case errors.Is(err, unix.EACCES), errors.Is(err, unix.EPERM):
		return "permission"
	case errors.Is(err, ErrDurableIdentity):
		return "durable_identity_unavailable"
	case errors.Is(err, ErrReclaimScope):
		return "scope_unavailable"
	case errors.Is(err, ErrReclaimIdentity):
		return "identity_changed"
	case errors.Is(err, ErrReclaimLinks):
		return "hardlink_or_nonregular"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "interrupted"
	default:
		return "filesystem_error"
	}
}

// ObserveSourceDirectory inventories at most limit entries on the same
// filesystem. Only the surviving parent is used after a complete deletion.
func ObserveSourceDirectory(ctx context.Context, root, path string, limit int) (*SourceDirectory, error) {
	proof, parent, err := observeSourceParent(root, path)
	if err != nil {
		return nil, err
	}
	defer unix.Close(parent)
	name := filepath.Base(path)
	fd, err := openWithin(parent, name, safeDir, 0)
	if err != nil {
		return nil, err
	}
	d := &SourceDirectory{Proof: proof, file: os.NewFile(uintptr(fd), "source directory"), path: path}
	if err := walkSourceDirectory(ctx, d.file, path, 1, &limit, &d.Files); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

func walkSourceDirectory(ctx context.Context, dir *os.File, absolute string, depth int, remaining *int, files *[]string) error {
	if depth > 8 {
		return ErrReclaimScope
	}
	before, err := dir.Stat()
	if err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := dir.ReadDir(min(*remaining+1, 16))
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if len(entries) > *remaining {
			return ErrReclaimScope
		}
		*remaining -= len(entries)
		for _, entry := range entries {
			child, err := openWithin(int(dir.Fd()), entry.Name(), unix.O_PATH, 0)
			if err != nil {
				return err
			}
			var st unix.Stat_t
			err = unix.Fstat(child, &st)
			unix.Close(child)
			if err != nil {
				return err
			}
			path := filepath.Join(absolute, entry.Name())
			switch st.Mode & unix.S_IFMT {
			case unix.S_IFREG:
				*files = append(*files, path)
			case unix.S_IFDIR:
				fd, err := openWithin(int(dir.Fd()), entry.Name(), safeDir, 0)
				if err != nil {
					return err
				}
				sub := os.NewFile(uintptr(fd), "source directory")
				err = walkSourceDirectory(ctx, sub, path, depth+1, remaining, files)
				sub.Close()
				if err != nil {
					return err
				}
			default:
				return ErrReclaimScope
			}
		}
		if len(entries) == 0 {
			break
		}
	}
	after, err := dir.Stat()
	if err != nil {
		return err
	}
	if !before.ModTime().Equal(after.ModTime()) || !os.SameFile(before, after) {
		return ErrReclaimIdentity
	}
	return nil
}

// Contains confirms the observed file is reachable from the pinned
// inventory, even if a concurrent operation changed the directory name.
func (d *SourceDirectory) Contains(path string, info os.FileInfo) bool {
	relative, err := filepath.Rel(d.path, path)
	if err != nil || !filepath.IsLocal(relative) {
		return false
	}
	fd, err := openWithin(int(d.file.Fd()), relative, unix.O_PATH, 0)
	if err != nil {
		return false
	}
	file := os.NewFile(uintptr(fd), "source")
	defer file.Close()
	actual, err := file.Stat()
	return err == nil && os.SameFile(info, actual)
}

func (d *SourceDirectory) Removed() bool {
	var st unix.Stat_t
	if unix.Fstat(int(d.file.Fd()), &st) != nil || st.Nlink != 0 {
		return false
	}
	parent, err := d.Proof.openParent()
	if err != nil {
		return false
	}
	defer unix.Close(parent)
	fd, err := openWithin(parent, filepath.Base(d.path), unix.O_PATH, 0)
	if err == nil {
		unix.Close(fd)
	}
	return errors.Is(err, unix.ENOENT)
}

func (o *SourceObservation) LostLink() bool {
	var st unix.Stat_t
	return unix.Fstat(int(o.file.Fd()), &st) == nil && uint64(st.Nlink)+1 == o.links
}

// SourceRootIdentity observes a configured directory without traversing children
// or reading original bytes. A caller must compare a previously pinned identity.
func SourceRootIdentity(path string) (DirectoryIdentity, error) {
	fd, err := openDirectoryPath(path)
	if err != nil {
		return DirectoryIdentity{}, err
	}
	defer unix.Close(fd)
	id, err := directoryIdentity(fd)
	if err != nil {
		return DirectoryIdentity{}, err
	}
	if !id.DirectoryIdentity.valid() {
		return DirectoryIdentity{}, ErrDurableIdentity
	}
	return id.DirectoryIdentity, nil
}
