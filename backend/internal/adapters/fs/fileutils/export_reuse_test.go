package fileutils

import "os"

// HoldMoveRemovalForTest installs a test-only hook. The returned function must
// run after the held removal completes; MoveByCopy captures it before copying.
func HoldMoveRemovalForTest(entered chan<- struct{}, release <-chan struct{}, done chan<- struct{}, fail bool) func() {
	previous := removeMovedSource
	removeMovedSource = func(path string) error {
		close(entered)
		<-release
		var err error
		if fail {
			err = os.ErrPermission
		} else {
			err = previous(path)
		}
		close(done)
		return err
	}
	return func() { removeMovedSource = previous }
}
