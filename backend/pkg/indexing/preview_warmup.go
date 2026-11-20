package indexing

import "os"

// MatchesPreviewWarmupVersion compares the opened file with B's indexed stat
// version. The caller supplies a descriptor stat so no path is reopened here.
func MatchesPreviewWarmupVersion(info os.FileInfo, version string) bool {
	return version != "" && versionFromFileInfo(info, "").String() == version
}
