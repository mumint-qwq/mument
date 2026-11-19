// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package indexing

import "sync/atomic"

// directoryReadTestHook is nil in production. Tests hold an old directory
// snapshot after Readdir but before its items are processed.
var directoryReadTestHook atomic.Pointer[func(stage, source, indexPath string)]

func SetDirectoryReadTestHookForTesting(hook func(stage, source, indexPath string)) {
	if hook == nil {
		directoryReadTestHook.Store(nil)
		return
	}
	directoryReadTestHook.Store(&hook)
}

func runDirectoryReadTestHook(stage, source, indexPath string) {
	if hook := directoryReadTestHook.Load(); hook != nil {
		(*hook)(stage, source, indexPath)
	}
}
