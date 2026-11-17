package diagnostics

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testWriterConfig() WriterConfig {
	cfg := DefaultWriterConfig()
	cfg.FlushInterval = 10 * time.Millisecond
	return cfg
}

func line(n int) []byte {
	return []byte(fmt.Sprintf(`{"n":%d,"pad":"%s"}`, n, strings.Repeat("x", 20)))
}

func TestWriterWritesNDJSONAndFlushes(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(dir, testWriterConfig(), t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if !w.Enqueue([][]byte{line(i)}, 1) {
			t.Fatalf("enqueue %d rejected", i)
		}
	}
	w.Close(2 * time.Second)
	data, err := os.ReadFile(filepath.Join(dir, currentFileName))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Count(string(data), "\n")
	if lines != 10 {
		t.Fatalf("expected 10 lines, got %d: %q", lines, data)
	}
	stats := w.Stats()
	if stats.WrittenEvents != 10 {
		t.Errorf("written=%d want 10", stats.WrittenEvents)
	}
	if stats.State != WriterStateReady {
		t.Errorf("state=%s", stats.State)
	}
}

func TestWriterRotatesAndBoundsFiles(t *testing.T) {
	dir := t.TempDir()
	cfg := testWriterConfig()
	cfg.FileMaxBytes = 150
	cfg.RotatedFiles = 3
	w, err := NewWriter(dir, cfg, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		w.Enqueue([][]byte{line(i)}, 1)
	}
	w.Close(2 * time.Second)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name != currentFileName && !rotatedNameRe.MatchString(name) {
			t.Errorf("unexpected file %q", name)
			continue
		}
		files++
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > cfg.FileMaxBytes {
			t.Errorf("%s is %d bytes, over %d", name, info.Size(), cfg.FileMaxBytes)
		}
	}
	if files > 4 {
		t.Errorf("expected at most 4 files, got %d", files)
	}
	if w.Stats().Rotations == 0 {
		t.Error("expected at least one rotation")
	}
}

func TestWriterRetentionAndStrictFileNames(t *testing.T) {
	dir := t.TempDir()
	cfg := testWriterConfig()
	cfg.Retention = time.Hour
	w, err := NewWriter(dir, cfg, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	w.Close(time.Second)

	oldName := "quantum-diag-0000000000000000001-1.ndjson"
	oldPath := filepath.Join(dir, oldName)
	if err := os.WriteFile(oldPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	recentName := "quantum-diag-0000000000000000002-2.ndjson"
	if err := os.WriteFile(filepath.Join(dir, recentName), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	w2, err := NewWriter(dir, cfg, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	w2.Close(time.Second)
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf("old rotated file was not removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, recentName)); err != nil {
		t.Errorf("recent rotated file removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Errorf("unrelated file removed: %v", err)
	}
}

func TestWriterRepairsIncompleteTailSeparator(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, currentFileName)
	if err := os.WriteFile(path, []byte(`{"partial":`), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := NewWriter(dir, testWriterConfig(), t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	w.Enqueue([][]byte{[]byte(`{"ok":1}`)}, 1)
	w.Close(2 * time.Second)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "{&quot;partial&quot;") && !strings.HasPrefix(string(data), `{"partial":
`) {
		t.Fatalf("partial tail was not separated: %q", data)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Fatalf("file does not end with a newline: %q", data)
	}
}

func TestWriterRejectsAfterCloseAndBadDirectory(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(dir, testWriterConfig(), t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	w.Close(time.Second)
	if w.Enqueue([][]byte{line(1)}, 1) {
		t.Error("Enqueue accepted after Close")
	}
	filePath := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWriter(filePath, testWriterConfig(), t.Logf); err == nil {
		t.Error("NewWriter accepted a regular file as directory")
	}
}

func TestWriterConcurrentEnqueue(t *testing.T) {
	dir := t.TempDir()
	cfg := testWriterConfig()
	cfg.MaxQueueBatches = 64
	w, err := NewWriter(dir, cfg, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int64
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if w.Enqueue([][]byte{line(base*100 + i)}, 1) {
					accepted.Add(1)
				}
			}
		}(worker)
	}
	wg.Wait()
	w.Close(3 * time.Second)
	data, err := os.ReadFile(filepath.Join(dir, currentFileName))
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	count := 0
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) != "" {
			count++
		}
	}
	if int64(count) != accepted.Load() {
		t.Errorf("wrote %d lines, accepted %d", count, accepted.Load())
	}
}
