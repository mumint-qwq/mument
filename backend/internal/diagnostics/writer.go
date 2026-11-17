package diagnostics

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const (
	WriterStateDisabled = "disabled"
	WriterStateReady    = "ready"
	WriterStateDegraded = "degraded"

	currentFileName = "quantum-diag-current.ndjson"
	rotatedPattern  = `^quantum-diag-[0-9]{10,19}-[0-9]+\.ndjson$`
)

var rotatedNameRe = regexp.MustCompile(rotatedPattern)

// WriterConfig bounds the single asynchronous writer.
type WriterConfig struct {
	MaxQueueBatches int
	MaxQueueBytes   int64
	FileMaxBytes    int64
	RotatedFiles    int
	Retention       time.Duration
	FlushInterval   time.Duration
	ReopenMin       time.Duration
	ReopenMax       time.Duration
	// CurrentMaxAge bounds how long records can stay in the active file before
	// time-based rotation. Retention applies to rotated files; rotating daily
	// keeps the active file from hiding records older than Retention.
	CurrentMaxAge time.Duration
}

// DefaultWriterConfig matches the v1 contract.
func DefaultWriterConfig() WriterConfig {
	return WriterConfig{
		MaxQueueBatches: 64,
		MaxQueueBytes:   4 * 1024 * 1024,
		FileMaxBytes:    8 * 1024 * 1024,
		RotatedFiles:    3,
		Retention:       7 * 24 * time.Hour,
		FlushInterval:   500 * time.Millisecond,
		ReopenMin:       5 * time.Second,
		ReopenMax:       60 * time.Second,
		CurrentMaxAge:   24 * time.Hour,
	}
}

type queuedBatch struct {
	lines  [][]byte
	events int
	bytes  int64
}

// WriterStats is safe to expose through GET /diagnostics/status.
type WriterStats struct {
	State         string    `json:"writer_state"`
	QueuedBatches int       `json:"queued_batches"`
	QueuedBytes   int64     `json:"queued_bytes"`
	WrittenEvents uint64    `json:"written_events"`
	DroppedEvents uint64    `json:"dropped_events"`
	WriteErrors   uint64    `json:"write_errors"`
	Rotations     uint64    `json:"rotations"`
	LastFlushAt   time.Time `json:"last_flush_at,omitempty"`
	Dir           string    `json:"-"`
}

// Writer owns one goroutine, one active file and up to RotatedFiles rotated
// files. Enqueue never waits for disk and never spawns per-request goroutines.
type Writer struct {
	cfg  WriterConfig
	dir  string
	logf func(format string, args ...any)

	queue chan queuedBatch
	stop  chan struct{}
	done  chan struct{}

	qmu           sync.Mutex
	closed        bool
	queuedBatches int
	queuedBytes   int64

	// stateMu protects only the short state string. File/buffer fields belong
	// to the writer goroutine; Enqueue and Stats must never wait for disk I/O.
	stateMu      sync.RWMutex
	state        string
	file         *os.File
	buf          *bufio.Writer
	currentSize  int64
	currentStart time.Time
	pending      int
	backoff      time.Duration

	written   atomic.Uint64
	dropped   atomic.Uint64
	writeErrs atomic.Uint64
	rotations atomic.Uint64
	lastFlush atomic.Int64
	rotated   atomic.Uint64
}

// NewWriter validates the directory, opens the active file and starts the
// writer goroutine.
func NewWriter(dir string, cfg WriterConfig, logf func(string, ...any)) (*Writer, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if cfg.MaxQueueBatches <= 0 || cfg.MaxQueueBytes <= 0 || cfg.FileMaxBytes <= 0 {
		return nil, fmt.Errorf("invalid writer limits")
	}
	if err := PrepareDir(dir); err != nil {
		return nil, err
	}
	w := &Writer{
		cfg:     cfg,
		dir:     dir,
		logf:    logf,
		queue:   make(chan queuedBatch, cfg.MaxQueueBatches),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		state:   WriterStateReady,
		backoff: cfg.ReopenMin,
	}
	if err := w.openCurrent(); err != nil {
		return nil, err
	}
	if err := w.cleanup(true); err != nil {
		w.logf("diagnostics: startup cleanup failed: %v", err)
	}
	go w.run()
	return w, nil
}

// Enqueue accepts one pre-encoded batch without waiting for disk.
func (w *Writer) Enqueue(lines [][]byte, events int) bool {
	if len(lines) == 0 {
		return true
	}
	var bytes int64
	for _, line := range lines {
		bytes += int64(len(line))
	}
	// Reserve queue accounting first, then publish to the channel. The only
	// shared lock here is qmu; currentState is a short state-only RLock and does
	// not touch file, buffer, Flush or Close.
	w.qmu.Lock()
	if w.closed || w.currentState() != WriterStateReady {
		w.qmu.Unlock()
		return false
	}
	if w.queuedBatches >= w.cfg.MaxQueueBatches || w.queuedBytes+bytes > w.cfg.MaxQueueBytes {
		w.qmu.Unlock()
		return false
	}
	w.queuedBatches++
	w.queuedBytes += bytes
	w.qmu.Unlock()

	select {
	case w.queue <- queuedBatch{lines: lines, events: events, bytes: bytes}:
		return true
	default:
		w.qmu.Lock()
		w.queuedBatches--
		w.queuedBytes -= bytes
		w.qmu.Unlock()
		return false
	}
}

func (w *Writer) currentState() string {
	w.stateMu.RLock()
	defer w.stateMu.RUnlock()
	return w.state
}

func (w *Writer) setState(next string) {
	w.stateMu.Lock()
	w.state = next
	w.stateMu.Unlock()
}

// Stats returns a consistent-enough snapshot for the status endpoint.
func (w *Writer) Stats() WriterStats {
	w.qmu.Lock()
	queuedBatches := w.queuedBatches
	queuedBytes := w.queuedBytes
	w.qmu.Unlock()
	state := w.currentState()
	stats := WriterStats{
		State:         state,
		QueuedBatches: queuedBatches,
		QueuedBytes:   queuedBytes,
		WrittenEvents: w.written.Load(),
		DroppedEvents: w.dropped.Load(),
		WriteErrors:   w.writeErrs.Load(),
		Rotations:     w.rotations.Load(),
		Dir:           w.dir,
	}
	if nanos := w.lastFlush.Load(); nanos > 0 {
		stats.LastFlushAt = time.Unix(0, nanos).UTC()
	}
	return stats
}

func (w *Writer) run() {
	defer close(w.done)
	ticker := time.NewTicker(w.cfg.FlushInterval)
	defer ticker.Stop()
	var reopen *time.Timer
	var reopenC <-chan time.Time
	lastCleanup := time.Now()

	for {
		select {
		case batch := <-w.queue:
			w.handleBatch(batch)
		case <-ticker.C:
			if err := w.rotateIfOld(); err != nil {
				w.degrade(err)
			}
			w.flush()
			if time.Since(lastCleanup) >= time.Hour {
				if err := w.cleanup(false); err != nil {
					w.logf("diagnostics: cleanup failed: %v", err)
				}
				lastCleanup = time.Now()
			}
		case <-reopenC:
			w.tryReopen()
			reopenC = nil
			if w.currentState() == WriterStateDegraded {
				reopen = time.NewTimer(w.backoff)
				reopenC = reopen.C
			}
		case <-w.stop:
			if reopen != nil {
				reopen.Stop()
			}
			w.drain(2 * time.Second)
			w.flush()
			w.closeFile()
			return
		}
		if reopenC == nil && w.currentState() == WriterStateDegraded {
			reopen = time.NewTimer(w.backoff)
			reopenC = reopen.C
		}
	}
}

func (w *Writer) handleBatch(batch queuedBatch) {
	w.qmu.Lock()
	w.queuedBatches--
	w.queuedBytes -= batch.bytes
	w.qmu.Unlock()

	if w.currentState() != WriterStateReady {
		w.dropped.Add(uint64(batch.events))
		return
	}
	accepted := 0
	for _, line := range batch.lines {
		if w.currentSize+int64(len(line))+1 > w.cfg.FileMaxBytes {
			if err := w.rotate(); err != nil {
				w.degrade(err)
				w.dropped.Add(uint64(batch.events - accepted))
				return
			}
		}
		if _, err := w.buf.Write(line); err != nil {
			w.degrade(err)
			w.dropped.Add(uint64(batch.events - accepted))
			return
		}
		if _, err := w.buf.Write([]byte("\n")); err != nil {
			w.degrade(err)
			w.dropped.Add(uint64(batch.events - accepted))
			return
		}
		w.currentSize += int64(len(line)) + 1
		w.pending++
		accepted++
	}
}

// flushBuffer is called only by the writer goroutine (or constructor-time
// rotation), so it never needs a disk-holding mutex shared with Enqueue.
func (w *Writer) flushBuffer() error {
	if w.buf == nil || w.pending == 0 {
		return nil
	}
	if err := w.buf.Flush(); err != nil {
		return err
	}
	pending := w.pending
	w.pending = 0
	w.written.Add(uint64(pending))
	w.lastFlush.Store(time.Now().UnixNano())
	return nil
}

func (w *Writer) flush() {
	if w.currentState() != WriterStateReady || w.buf == nil {
		return
	}
	if err := w.flushBuffer(); err != nil {
		w.writeErrs.Add(1)
		w.degrade(err)
	}
}

// degrade publishes the degraded state first, then drops pending buffered
// records and closes the file. It never Flushes: a blocked disk must not turn
// a health transition into an unbounded wait.
func (w *Writer) degrade(err error) {
	if w.currentState() == WriterStateDegraded {
		return
	}
	w.setState(WriterStateDegraded)
	pending := w.pending
	w.pending = 0
	w.dropped.Add(uint64(pending))
	w.buf = nil
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}
	w.writeErrs.Add(1)
	w.logf("diagnostics: writer degraded: %v", err)
}

func (w *Writer) tryReopen() {
	if err := w.openCurrent(); err != nil {
		w.backoff *= 2
		if w.backoff > w.cfg.ReopenMax {
			w.backoff = w.cfg.ReopenMax
		}
		w.logf("diagnostics: writer reopen failed: %v", err)
		return
	}
	w.setState(WriterStateReady)
	w.backoff = w.cfg.ReopenMin
}

func (w *Writer) rotateIfOld() error {
	if w.currentState() != WriterStateReady || w.currentSize <= 0 || w.currentStart.IsZero() {
		return nil
	}
	if w.cfg.CurrentMaxAge > 0 && time.Since(w.currentStart) >= w.cfg.CurrentMaxAge {
		return w.rotate()
	}
	return nil
}

func (w *Writer) openCurrent() error {
	path := filepath.Join(w.dir, currentFileName)
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("active diagnostics file must not be a symlink")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	size := info.Size()
	// A crash or disk-full can leave a partial line. Separate it from new
	// records without rewriting or deleting the original evidence.
	if size > 0 {
		reader, readErr := os.Open(path)
		if readErr != nil {
			_ = file.Close()
			return readErr
		}
		last := make([]byte, 1)
		_, readErr = reader.ReadAt(last, size-1)
		_ = reader.Close()
		if readErr != nil {
			_ = file.Close()
			return readErr
		}
		if last[0] != '\n' {
			if _, writeErr := file.Write([]byte("\n")); writeErr != nil {
				_ = file.Close()
				return writeErr
			}
			size++
		}
	}
	w.file = file
	w.buf = bufio.NewWriterSize(file, 64*1024)
	w.currentSize = size
	w.pending = 0
	w.currentStart = info.ModTime()
	if size == 0 {
		w.currentStart = time.Now()
	}
	return nil
}

func (w *Writer) rotate() error {
	if w.buf != nil {
		if err := w.flushBuffer(); err != nil {
			return err
		}
		w.buf = nil
	}
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			w.file = nil
			return err
		}
		w.file = nil
	}

	currentPath := filepath.Join(w.dir, currentFileName)
	name := fmt.Sprintf("quantum-diag-%d-%d.ndjson", time.Now().UTC().UnixNano(), w.rotated.Add(1))
	rotatedPath := filepath.Join(w.dir, name)
	if err := os.Rename(currentPath, rotatedPath); err != nil {
		return err
	}
	if err := os.Chmod(rotatedPath, 0o600); err != nil {
		return err
	}
	w.rotations.Add(1)
	if err := w.openCurrent(); err != nil {
		return err
	}
	if err := w.cleanup(false); err != nil {
		w.logf("diagnostics: rotation cleanup failed: %v", err)
	}
	return nil
}

func (w *Writer) cleanup(startup bool) error {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return err
	}
	type rotatedFile struct {
		name    string
		modTime time.Time
	}
	var files []rotatedFile
	for _, entry := range entries {
		if entry.IsDir() || !rotatedNameRe.MatchString(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, rotatedFile{name: entry.Name(), modTime: info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.Before(files[j].modTime) })
	now := time.Now()
	keep := w.cfg.RotatedFiles
	for i, file := range files {
		tooOld := now.Sub(file.modTime) > w.cfg.Retention
		tooMany := len(files)-i > keep
		if tooOld || tooMany {
			if err := os.Remove(filepath.Join(w.dir, file.name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				w.logf("diagnostics: could not remove rotated file: %v", err)
			}
		}
	}
	_ = startup
	return nil
}

func (w *Writer) drain(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case batch := <-w.queue:
			w.handleBatch(batch)
		default:
			return
		}
	}
}

// Close stops accepting batches and waits up to timeout for the writer
// goroutine. It never performs a second Flush/Close from the caller: a blocked
// disk must not hold shutdown past the caller's budget.
func (w *Writer) Close(timeout time.Duration) {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	w.qmu.Lock()
	if w.closed {
		w.qmu.Unlock()
		return
	}
	w.closed = true
	w.qmu.Unlock()
	close(w.stop)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-w.done:
	case <-timer.C:
	}
}

func (w *Writer) closeFile() {
	if w.buf != nil {
		_ = w.buf.Flush()
		w.buf = nil
	}
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}
}
