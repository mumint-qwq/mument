// Added for mument 007-C. See docs/development/007c/DESIGN.md.
package cacheledger

import "os"

// Version is an observation, never a permanent content identifier. Weak
// platforms may generate previews but cannot claim persistent identity reuse.
type Version struct {
	Size    int64
	MtimeNS int64
	CtimeNS int64
	Dev     uint64
	Ino     uint64
	Strong  bool
}

func Observe(path string) (Version, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Version{}, err
	}
	return FromFileInfo(info), nil
}

// RenameCompatible is only meaningful after an observed successful os.Rename.
// It deliberately does not prove external moves, copies, or arbitrary writes
// by another process inside the rename window.
func (v Version) RenameCompatible(after Version) bool {
	return v.Strong && after.Strong && v.Dev == after.Dev && v.Ino == after.Ino &&
		v.Size == after.Size && v.MtimeNS == after.MtimeNS
}
