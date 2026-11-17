//go:build !linux || 386 || arm

// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.

package indexing

import (
	"os"
	"strconv"
)

const (
	versionKnownSize uint8 = 1 << iota
	versionKnownMtimeNS
	versionKnownCtimeNS
	versionKnownDev
	versionKnownIno
)

type fileVersion struct {
	Known   uint8
	Size    int64
	MtimeNS int64
	CtimeNS int64
	Dev     uint64
	Ino     uint64
}

// versionFromFileInfo uses only portable fields. Weak filesystems therefore get
// a conservative size+mtime observation, never a fabricated inode identity.
func versionFromFileInfo(info os.FileInfo, path string) fileVersion {
	if info == nil {
		return fileVersion{}
	}
	return fileVersion{
		Known:   versionKnownSize | versionKnownMtimeNS,
		Size:    info.Size(),
		MtimeNS: info.ModTime().UnixNano(),
	}
}

func (v fileVersion) String() string {
	b := make([]byte, 0, 96)
	b = append(b, 'v', '1', ':')
	b = strconv.AppendUint(b, uint64(v.Known), 16)
	b = append(b, ':')
	b = strconv.AppendInt(b, v.Size, 10)
	b = append(b, ':')
	b = strconv.AppendInt(b, v.MtimeNS, 10)
	b = append(b, ':')
	b = strconv.AppendInt(b, v.CtimeNS, 10)
	b = append(b, ':')
	b = strconv.AppendUint(b, v.Dev, 10)
	b = append(b, ':')
	b = strconv.AppendUint(b, v.Ino, 10)
	return string(b)
}
