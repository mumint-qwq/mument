package diagnostics

import (
	"bufio"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type blockingWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(p), nil
}

func TestWriterEnqueueDoesNotWaitForSlowRotation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, currentFileName), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	slow := &blockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
	w := &Writer{
		cfg:   DefaultWriterConfig(),
		dir:   dir,
		state: WriterStateReady,
		queue: make(chan queuedBatch, 64),
		logf:  t.Logf,
	}
	w.buf = bufio.NewWriter(slow)
	if _, err := w.buf.WriteString("pending\n"); err != nil {
		t.Fatal(err)
	}
	w.pending = 1
	w.currentSize = 8

	rotated := make(chan struct{})
	go func() {
		_ = w.rotate()
		close(rotated)
	}()
	<-slow.entered

	start := time.Now()
	accepted := w.Enqueue([][]byte{[]byte("{\"a\":1}")}, 1)
	elapsed := time.Since(start)
	if !accepted {
		t.Fatal("enqueue rejected while rotation was waiting for disk")
	}
	if elapsed > 100*time.Millisecond {
		t.Fatalf("enqueue waited for slow rotation: %s", elapsed)
	}
	close(slow.release)
	<-rotated
}

func TestWriterCloseDoesNotWaitForBlockedFlush(t *testing.T) {
	slow := &blockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
	w := &Writer{
		cfg:   WriterConfig{FlushInterval: time.Hour, CurrentMaxAge: time.Hour, MaxQueueBatches: 1, MaxQueueBytes: 1, FileMaxBytes: 1},
		dir:   t.TempDir(),
		state: WriterStateReady,
		queue: make(chan queuedBatch, 1),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
		logf:  t.Logf,
	}
	w.buf = bufio.NewWriter(slow)
	if _, err := w.buf.WriteString("pending\n"); err != nil {
		t.Fatal(err)
	}
	w.pending = 1
	go w.run()

	start := time.Now()
	w.Close(100 * time.Millisecond)
	elapsed := time.Since(start)
	if elapsed > time.Second {
		t.Fatalf("Close waited for blocked flush: %s", elapsed)
	}
	close(slow.release)
	select {
	case <-w.done:
	case <-time.After(2 * time.Second):
		t.Fatal("writer did not finish after disk was released")
	}
}

func TestWriterRejectsSymlinkCurrentFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "outside.ndjson")
	if err := os.WriteFile(target, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, currentFileName)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := NewWriter(dir, DefaultWriterConfig(), t.Logf); err == nil {
		t.Fatal("NewWriter followed a symlinked active file")
	}
}

func TestWriterRotatesActiveFileByAge(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, currentFileName)
	if err := os.WriteFile(current, []byte("{\"old\":1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(current, old, old); err != nil {
		t.Fatal(err)
	}
	cfg := testWriterConfig()
	cfg.CurrentMaxAge = time.Hour
	w, err := NewWriter(dir, cfg, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if !w.Enqueue([][]byte{[]byte("{\"new\":2}")}, 1) {
		t.Fatal("enqueue rejected")
	}
	deadline := time.Now().Add(2 * time.Second)
	for w.Stats().Rotations == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	w.Close(2 * time.Second)
	if w.Stats().Rotations == 0 {
		t.Fatal("old active file was not rotated by age")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if entry.Name() == currentFileName || !rotatedNameRe.MatchString(entry.Name()) {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		if readErr == nil && len(data) > 0 {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no non-empty rotated file after age rotation")
	}
}
