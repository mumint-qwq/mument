package indexing

import "sync/atomic"

// refreshTestHook is called at named stages of a directory scan. It is nil in
// production, where the only cost is one atomic pointer load per directory.
// Tests use it to hold a scan open and simulate a late snapshot.
var refreshTestHook atomic.Pointer[func(stage, source, indexPath string)]

// SetRefreshTestHookForTesting installs (or clears) the directory-scan test hook.
func SetRefreshTestHookForTesting(hook func(stage, source, indexPath string)) {
	if hook == nil {
		refreshTestHook.Store(nil)
		return
	}
	refreshTestHook.Store(&hook)
}
