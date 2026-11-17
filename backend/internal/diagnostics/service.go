package diagnostics

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"
)

// AcceptError is an HTTP-safe rejection. It never contains submitted content.
type AcceptError struct {
	Status     int    `json:"-"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	RetryAfter int    `json:"-"`
}

func (e *AcceptError) Error() string { return e.Message }

// AcceptResult is the 202 body. queued=true only means the batch entered the
// writer queue, not that it reached disk.
type AcceptResult struct {
	BatchRef     string `json:"batch_ref"`
	Queued       bool   `json:"queued"`
	Deduplicated bool   `json:"deduplicated"`
	WriterState  string `json:"writer_state"`
}

type StatusLimits struct {
	MaxEvents      int   `json:"max_events"`
	MaxEventBytes  int   `json:"max_event_bytes"`
	MaxBodyBytes   int   `json:"max_body_bytes"`
	MaxBatches     int   `json:"max_queued_batches"`
	MaxQueueBytes  int64 `json:"max_queued_bytes"`
	FileMaxBytes   int64 `json:"file_max_bytes"`
	RotatedFiles   int   `json:"rotated_files"`
	RetentionHours int   `json:"retention_hours"`
}

type StatusCounters struct {
	ReceivedBatches uint64 `json:"received_batches"`
	ReceivedEvents  uint64 `json:"received_events"`
	QueuedBatches   int    `json:"queued_batches"`
	QueuedBytes     int64  `json:"queued_bytes"`
	WrittenEvents   uint64 `json:"written_events"`
	DroppedEvents   uint64 `json:"dropped_events"`
	Deduplicated    uint64 `json:"deduplicated_batches"`
	RejectedBatches uint64 `json:"rejected_batches"`
	InvalidBatches  uint64 `json:"invalid_batches"`
	RateLimited     uint64 `json:"rate_limited"`
	WriteErrors     uint64 `json:"write_errors"`
	Rotations       uint64 `json:"rotations"`
	LastReceivedAt  string `json:"last_received_at,omitempty"`
	LastFlushAt     string `json:"last_flush_at,omitempty"`
}

// Status is the exact fixed status schema exposed to authenticated users.
type Status struct {
	Enabled        bool           `json:"enabled"`
	SchemaVersion  int            `json:"schema_version"`
	WriterState    string         `json:"writer_state"`
	Limits         StatusLimits   `json:"limits"`
	Counters       StatusCounters `json:"counters"`
	DisabledReason string         `json:"disabled_reason,omitempty"`
}

// Options configures a diagnostics service without reading globals.
type Options struct {
	Config       Config
	WriterConfig WriterConfig
	Build        string
	Logf         func(format string, args ...any)
}

// Service owns the receiver, writer, limiter and dedupe state.
type Service struct {
	cfg      Config
	writer   *Writer
	limiter  *Limiter
	dedupe   *Dedupe
	instance string
	build    string
	enabled  bool
	reason   string
	logf     func(string, ...any)

	lastReceived atomic.Int64
	received     atomic.Uint64
	receivedEv   atomic.Uint64
	deduped      atomic.Uint64
	rejected     atomic.Uint64
	invalid      atomic.Uint64
	rateLimited  atomic.Uint64
	dropped      atomic.Uint64
}

// NewFromEnv builds the service from environment variables. A bad environment
// yields a disabled service plus an error for a bounded startup log line.
func NewFromEnv(getenv func(string) string, build string, logf func(string, ...any)) (*Service, error) {
	cfg, err := LoadConfig(getenv)
	if err != nil {
		disabled := &Service{cfg: Config{Dir: cfg.Dir}, build: build, logf: logf, enabled: false, reason: err.Error(), instance: newInstance()}
		disabled.limiter = NewLimiter(DefaultRateLimits(), nil)
		disabled.dedupe = NewDedupe(1024, 10*time.Minute, nil)
		return disabled, err
	}
	return NewService(Options{Config: cfg, WriterConfig: DefaultWriterConfig(), Build: build, Logf: logf})
}

// NewService creates a service. If the directory/writer cannot be prepared the
// returned service is disabled and the error is informational.
func NewService(opts Options) (*Service, error) {
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if opts.WriterConfig.MaxQueueBytes == 0 {
		opts.WriterConfig = DefaultWriterConfig()
	}
	svc := &Service{
		cfg:      opts.Config,
		build:    opts.Build,
		logf:     logf,
		enabled:  opts.Config.Enabled,
		instance: newInstance(),
	}
	svc.limiter = NewLimiter(DefaultRateLimits(), nil)
	svc.dedupe = NewDedupe(1024, 10*time.Minute, nil)
	if !svc.enabled {
		svc.reason = "disabled by configuration"
		return svc, nil
	}
	writer, err := NewWriter(opts.Config.Dir, opts.WriterConfig, logf)
	if err != nil {
		svc.enabled = false
		svc.reason = "diagnostics storage unavailable"
		return svc, err
	}
	svc.writer = writer
	return svc, nil
}

func newInstance() string {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(bytes)
}

// Enabled reports whether the receiver and writer are active.
func (s *Service) Enabled() bool { return s != nil && s.enabled }

// Config returns the loaded configuration (origins are needed by the handler).
func (s *Service) Config() Config { return s.cfg }

// Instance returns the process-local server instance (16 hex).
func (s *Service) Instance() string { return s.instance }

func (s *Service) actorRef(userID uint64) string {
	sum := sha256.Sum256([]byte("quantum-diagnostics-v1|" + strconv.FormatUint(userID, 10)))
	return hex.EncodeToString(sum[:16])
}

// Accept validates, deduplicates and enqueues one client batch.
func (s *Service) Accept(userID uint64, body []byte) (*AcceptResult, *AcceptError) {
	if !s.Enabled() {
		return nil, &AcceptError{Status: 404, Code: "diagnostics_disabled", Message: "diagnostics receiver is not enabled"}
	}
	actorRef := s.actorRef(userID)
	if allowed, retry := s.limiter.Allow(actorRef); !allowed {
		s.rateLimited.Add(1)
		s.rejected.Add(1)
		return nil, &AcceptError{Status: 429, Code: "rate_limited", Message: "too many diagnostics batches", RetryAfter: retry}
	}
	batch, err := ValidateBatch(body)
	if err != nil {
		s.invalid.Add(1)
		s.rejected.Add(1)
		status := 422
		code := "invalid_batch"
		message := "diagnostics batch rejected"
		if validation, ok := err.(*ValidationError); ok {
			status = validation.Status
			code = validation.Code
			message = validation.Message
		}
		return nil, &AcceptError{Status: status, Code: code, Message: message}
	}
	if s.dedupe.Seen(actorRef, batch.ClientSession, batch.BatchRef) {
		s.deduped.Add(1)
		state := WriterStateDisabled
		if s.writer != nil {
			state = s.writer.currentState()
		}
		return &AcceptResult{BatchRef: batch.BatchRef, Queued: true, Deduplicated: true, WriterState: state}, nil
	}
	lines, events, err := s.encodeClientBatch(actorRef, batch)
	if err != nil {
		s.invalid.Add(1)
		s.rejected.Add(1)
		return nil, &AcceptError{Status: 422, Code: "encode_failed", Message: "diagnostics batch could not be encoded"}
	}
	if s.writer == nil || !s.writer.Enqueue(lines, events) {
		s.rejected.Add(1)
		s.dropped.Add(uint64(events))
		state := WriterStateDegraded
		if s.writer != nil {
			state = s.writer.currentState()
		}
		return nil, &AcceptError{Status: 503, Code: "writer_unavailable", Message: "diagnostics storage is not accepting batches (state " + state + ")"}
	}
	s.dedupe.Mark(actorRef, batch.ClientSession, batch.BatchRef)
	s.received.Add(1)
	s.receivedEv.Add(uint64(events))
	s.lastReceived.Store(time.Now().UnixNano())
	return &AcceptResult{BatchRef: batch.BatchRef, Queued: true, Deduplicated: false, WriterState: s.writer.currentState()}, nil
}

type clientRecord struct {
	SchemaVersion  int            `json:"schema_version"`
	Origin         string         `json:"origin"`
	ReceivedAt     string         `json:"received_at"`
	ServerInstance string         `json:"server_instance"`
	ServerBuild    string         `json:"server_build"`
	ActorRef       string         `json:"actor_ref"`
	ClientSession  string         `json:"client_session"`
	BatchRef       string         `json:"batch_ref"`
	ClientBuild    ClientBuild    `json:"client_build"`
	Seq            int64          `json:"seq"`
	TMs            float64        `json:"t_ms"`
	Level          string         `json:"level"`
	Event          string         `json:"event"`
	LoadRef        *string        `json:"load_ref"`
	OperationRef   *string        `json:"operation_ref"`
	RequestRef     *string        `json:"request_ref"`
	IncidentRef    *string        `json:"incident_ref"`
	Data           map[string]any `json:"data"`
}

func (s *Service) encodeClientBatch(actorRef string, batch *Batch) ([][]byte, int, error) {
	receivedAt := time.Now().UTC().Format(time.RFC3339Nano)
	lines := make([][]byte, 0, len(batch.Events))
	for _, event := range batch.Events {
		record := clientRecord{
			SchemaVersion:  SchemaVersion,
			Origin:         "client",
			ReceivedAt:     receivedAt,
			ServerInstance: s.instance,
			ServerBuild:    s.build,
			ActorRef:       actorRef,
			ClientSession:  batch.ClientSession,
			BatchRef:       batch.BatchRef,
			ClientBuild:    batch.ClientBuild,
			Seq:            event.Seq,
			TMs:            event.TMs,
			Level:          event.Level,
			Event:          event.Name,
			LoadRef:        event.LoadRef,
			OperationRef:   event.OperationRef,
			RequestRef:     event.RequestRef,
			IncidentRef:    event.IncidentRef,
			Data:           event.Data,
		}
		line, err := json.Marshal(record)
		if err != nil {
			return nil, 0, err
		}
		lines = append(lines, line)
	}
	return lines, len(lines), nil
}

type serverRecord struct {
	SchemaVersion  int            `json:"schema_version"`
	Origin         string         `json:"origin"`
	ReceivedAt     string         `json:"received_at"`
	ServerInstance string         `json:"server_instance"`
	ServerBuild    string         `json:"server_build"`
	ActorRef       string         `json:"actor_ref"`
	Event          string         `json:"event"`
	Level          string         `json:"level"`
	RequestRef     string         `json:"request_ref"`
	Data           map[string]any `json:"data"`
}

// RecordListingSpan writes one server-origin span when the client sent a valid
// X-Quantum-Trace reference. It never reads paths, queries or tokens.
func (s *Service) RecordListingSpan(userID uint64, requestRef, api string, status int, elapsed time.Duration, knownItems *int, paged bool, outcome string) {
	if !s.Enabled() || s.writer == nil {
		return
	}
	if ValidateRequestRef(requestRef) == "" || (api != "resources" && api != "resources_list") {
		return
	}
	if status < 100 || status > 599 || elapsed < 0 {
		return
	}
	var known any
	if knownItems != nil && *knownItems >= 0 && *knownItems <= 1000000 {
		known = *knownItems
	}
	if outcome != "ok" && outcome != "error" {
		outcome = "ok"
	}
	record := serverRecord{
		SchemaVersion:  SchemaVersion,
		Origin:         "server",
		ReceivedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		ServerInstance: s.instance,
		ServerBuild:    s.build,
		ActorRef:       s.actorRef(userID),
		Event:          "server.listing_span",
		Level:          "info",
		RequestRef:     requestRef,
		Data: map[string]any{
			"api":                api,
			"status":             status,
			"handler_elapsed_ms": float64(elapsed.Microseconds()) / 1000,
			"known_items":        known,
			"paged":              paged,
			"outcome":            outcome,
		},
	}
	line, err := json.Marshal(record)
	if err != nil {
		s.logf("diagnostics: server span encode failed: %v", err)
		return
	}
	if !s.writer.Enqueue([][]byte{line}, 1) {
		s.dropped.Add(1)
	}
}

// DefaultStatusLimits is the fixed v1 status limit block.
func DefaultStatusLimits() StatusLimits {
	return StatusLimits{
		MaxEvents:      MaxEvents,
		MaxEventBytes:  MaxEventBytes,
		MaxBodyBytes:   MaxBodyBytes,
		MaxBatches:     64,
		MaxQueueBytes:  4 * 1024 * 1024,
		FileMaxBytes:   8 * 1024 * 1024,
		RotatedFiles:   3,
		RetentionHours: 168,
	}
}

// DisabledStatus is returned when the receiver is not configured.
func DisabledStatus() Status {
	return Status{
		Enabled:       false,
		SchemaVersion: SchemaVersion,
		WriterState:   WriterStateDisabled,
		Limits:        DefaultStatusLimits(),
	}
}

// Status returns fixed counters only; no paths, payloads or account names.
func (s *Service) Status() Status {
	status := DisabledStatus()
	status.Enabled = s.Enabled()
	status.DisabledReason = s.reason
	status.Counters = StatusCounters{
		ReceivedBatches: s.received.Load(),
		ReceivedEvents:  s.receivedEv.Load(),
		DroppedEvents:   s.dropped.Load(),
		Deduplicated:    s.deduped.Load(),
		RejectedBatches: s.rejected.Load(),
		InvalidBatches:  s.invalid.Load(),
		RateLimited:     s.rateLimited.Load(),
	}
	if s.writer != nil {
		stats := s.writer.Stats()
		status.WriterState = stats.State
		status.Counters.QueuedBatches = stats.QueuedBatches
		status.Counters.QueuedBytes = stats.QueuedBytes
		status.Counters.WrittenEvents = stats.WrittenEvents
		status.Counters.DroppedEvents += stats.DroppedEvents
		status.Counters.WriteErrors = stats.WriteErrors
		status.Counters.Rotations = stats.Rotations
		if !stats.LastFlushAt.IsZero() {
			status.Counters.LastFlushAt = stats.LastFlushAt.Format(time.RFC3339Nano)
		}
	}
	if nanos := s.lastReceived.Load(); nanos > 0 {
		status.Counters.LastReceivedAt = time.Unix(0, nanos).UTC().Format(time.RFC3339Nano)
	}
	return status
}

// Close drains the writer for up to timeout.
func (s *Service) Close(timeout time.Duration) {
	if s == nil || s.writer == nil {
		return
	}
	s.writer.Close(timeout)
}

// WriterStats is used by tests and the report tooling.
func (s *Service) WriterStats() WriterStats {
	if s.writer == nil {
		return WriterStats{State: WriterStateDisabled}
	}
	return s.writer.Stats()
}
