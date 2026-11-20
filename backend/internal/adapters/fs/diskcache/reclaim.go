package diskcache

import (
	"errors"
	"os"
)

var (
	ErrReclaimScope    = errors.New("scope_unavailable")
	ErrReclaimIdentity = errors.New("identity_changed")
	ErrReclaimBusy     = errors.New("cache_writer_busy")
	ErrReclaimLinks    = errors.New("hardlink_or_nonregular")
)

type DirectoryIdentity struct {
	Dev, Ino, Mount uint64
	Filesystem      string
}

type SourceProof struct {
	Root, Parent     string
	RootID, ParentID DirectoryIdentity
}

type ObjectStamp struct {
	Root                       DirectoryIdentity
	Dev, Ino                   uint64
	Size, Mtime, Ctime, Blocks int64
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
	name  string
}

func (d *SourceDirectory) Close() error { return d.file.Close() }
