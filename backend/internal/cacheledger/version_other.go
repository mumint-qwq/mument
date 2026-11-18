//go:build !linux || 386 || arm

// Added for mument 007-C.
package cacheledger

import "os"

func FromFileInfo(info os.FileInfo) Version {
	if info == nil {
		return Version{}
	}
	return Version{Size: info.Size(), MtimeNS: info.ModTime().UnixNano()}
}

func filesystemID(string) (string, bool) { return "", false }
