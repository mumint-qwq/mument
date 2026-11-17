//go:build linux && !386 && !arm

// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.

package indexing

import (
	"os"
	"strconv"
	"syscall"
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

func versionFromFileInfo(info os.FileInfo, path string) fileVersion {
	v := fileVersion{Size: info.Size(), MtimeNS: info.ModTime().UnixNano(), Known: versionKnownSize | versionKnownMtimeNS}
	if info == nil {
		return v
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok && path != "" {
		var s syscall.Stat_t
		if err := syscall.Stat(path, &s); err == nil {
			stat, ok = &s, true
		}
	}
	if ok {
		v.Size = stat.Size
		v.MtimeNS = stat.Mtim.Sec*1e9 + stat.Mtim.Nsec
		v.CtimeNS = stat.Ctim.Sec*1e9 + stat.Ctim.Nsec
		v.Dev = uint64(stat.Dev)
		v.Ino = stat.Ino
		v.Known |= versionKnownCtimeNS | versionKnownDev | versionKnownIno
	}
	return v
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
