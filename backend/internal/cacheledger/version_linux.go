//go:build linux && !386 && !arm && !cacheledger_portable

// Added for mument 007-C.
package cacheledger

import (
	"fmt"
	"os"
	"syscall"
)

func FromFileInfo(info os.FileInfo) Version {
	if info == nil {
		return Version{}
	}
	v := Version{Size: info.Size(), MtimeNS: info.ModTime().UnixNano()}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		v.CtimeNS = st.Ctim.Sec*1e9 + st.Ctim.Nsec
		v.Dev, v.Ino, v.Strong = uint64(st.Dev), st.Ino, true
	}
	return v
}

func filesystemID(path string) (string, bool) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return "", false
	}
	return fmt.Sprintf("%x:%v", stat.Type, stat.Fsid), true
}
