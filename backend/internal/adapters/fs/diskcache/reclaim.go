package diskcache

import (
	"errors"
	"os"
)

var (
	ErrReclaimScope    = errors.New("scope_unavailable")
	ErrDurableIdentity = errors.New("durable_identity_unavailable")
	ErrReclaimIdentity = errors.New("identity_changed")
	ErrReclaimBusy     = errors.New("cache_writer_busy")
	ErrReclaimLinks    = errors.New("hardlink_or_nonregular")
)

type DirectoryIdentity struct {
	Dev, Ino   uint64
	Birth      int64
	Filesystem string
}

func (id DirectoryIdentity) valid() bool {
	return id.Ino != 0 && id.Birth != 0 && id.Filesystem != ""
}

type SourceProof struct {
	Root, Parent     string
	RootID, ParentID DirectoryIdentity
	mount            uint64
}

type ObjectStamp struct {
	Root                              DirectoryIdentity
	Dev, Ino                          uint64
	Birth, Size, Mtime, Ctime, Blocks int64
}

func (a ObjectStamp) HasDurableIdentity() bool {
	return a.Root.valid() && a.Ino != 0 && a.Birth != 0
}

func (a ObjectStamp) SameRenamed(b ObjectStamp) bool {
	a.Ctime, b.Ctime = 0, 0
	return a == b
}

type SourceObservation struct {
	Info  os.FileInfo
	Proof SourceProof
	file  *os.File
	links uint64
	path  string
}

func (o *SourceObservation) Close() error { return o.file.Close() }

type SourceDirectory struct {
	Proof SourceProof
	Files []string
	file  *os.File
	path  string
}

func (d *SourceDirectory) Close() error { return d.file.Close() }
