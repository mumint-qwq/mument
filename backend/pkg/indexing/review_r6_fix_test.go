// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package indexing

import (
	"context"
	"testing"
	"time"
)

func TestR6CancelledWaiterDoesNotWaitForAnotherReader(t *testing.T) {
	imageHeaderSem <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = readImageHeader(ctx, "unused", fileVersion{})
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		<-imageHeaderSem
		t.Fatal("cancelled waiter blocked on the header slot")
	}
	<-imageHeaderSem
}
