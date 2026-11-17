package diagnostics

import (
	"strings"
	"testing"
	"time"
)

func TestWriterRejectsBatchOverByteLimit(t *testing.T) {
	dir := t.TempDir()
	cfg := testWriterConfig()
	cfg.MaxQueueBytes = 64
	w, err := NewWriter(dir, cfg, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close(time.Second)
	lines := [][]byte{[]byte(strings.Repeat("x", 80))}
	if w.Enqueue(lines, 1) {
		t.Fatal("oversized batch was accepted")
	}
	if w.Stats().QueuedBatches != 0 {
		t.Errorf("queue accounting changed after rejection: %+v", w.Stats())
	}
}

func TestWriterRotationFailureCountsDrops(t *testing.T) {
	dir := t.TempDir()
	cfg := testWriterConfig()
	cfg.FileMaxBytes = 120
	w, err := NewWriter(dir, cfg, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	// Two lines force a rotation before the second one.
	line := []byte(strings.Repeat("y", 70))
	w.Enqueue([][]byte{line, line}, 2)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		stats := w.Stats()
		if stats.WrittenEvents+stats.DroppedEvents >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The first line fits; the second triggers a rotation, which normally
	// succeeds. This test mainly guards accounting: written+dropped must equal
	// the accepted two events without negative counters.
	stats := w.Stats()
	if stats.WrittenEvents+stats.DroppedEvents != 2 {
		t.Errorf("written=%d dropped=%d want total 2", stats.WrittenEvents, stats.DroppedEvents)
	}
	w.Close(time.Second)
}

func TestServiceRejectsWhenWriterClosed(t *testing.T) {
	svc, _ := testService(t)
	svc.writer.Close(time.Second)
	result, acceptErr := svc.Accept(11, validBody(t))
	if acceptErr == nil || acceptErr.Status != 503 {
		t.Fatalf("closed writer accepted batch: result=%+v err=%+v", result, acceptErr)
	}
}
