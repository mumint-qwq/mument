//go:build linux

package cachetrial

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/diskcache"
	"golang.org/x/sys/unix"
)

type Options struct {
	CacheDir, Snapshot, FixtureEvidence string
	ExpectSnapshot                      string
	AfterResult                         int64
	AfterCache                          string
	MaxEntries, Samples                 int
	MaxBytes                            int64
	MaxTime                             time.Duration
}

type Report struct {
	StartedAt           time.Time     `json:"started_at"`
	FinishedAt          time.Time     `json:"finished_at"`
	EvidenceScope       string        `json:"evidence_scope"`
	SnapshotID          string        `json:"snapshot_id,omitempty"`
	Limits              OptionsReport `json:"limits"`
	Stop                string        `json:"stop,omitempty"`
	Ledger              LedgerSection `json:"ledger"`
	Inventory           Section       `json:"inventory"`
	MovesComplete       bool          `json:"move_coverage_complete"`
	PartitionAvailable  *uint64       `json:"partition_available_bytes,omitempty"`
	EstimatedCandidates int           `json:"estimated_candidates"`
	EstimatedLogical    int64         `json:"estimated_candidate_logical_bytes"`
	EstimatedAllocated  int64         `json:"estimated_candidate_allocated_bytes"`
	ExecutableDeletes   int           `json:"executable_deletes"`
}

type OptionsReport struct {
	Entries      int   `json:"entries_per_section"`
	Bytes        int64 `json:"logical_bytes_per_section"`
	Milliseconds int64 `json:"milliseconds"`
	Samples      int   `json:"samples_per_section"`
}

type LedgerSection struct {
	Section
	// ResultKeyCoverage tracks the snapshot registry independently of disk probes.
	ResultKeyCoverage bool `json:"result_key_coverage_from_start"`
}

type Section struct {
	Complete        bool              `json:"window_complete"`
	Coverage        bool              `json:"coverage_from_start"`
	Gaps            bool              `json:"coverage_gaps"`
	AfterRow        int64             `json:"after_result,omitempty"`
	NextRow         int64             `json:"next_result"`
	NextCache       string            `json:"next_cache,omitempty"`
	Stop            string            `json:"stop,omitempty"`
	Visited         int               `json:"visited"`
	Classes         map[string]*Tally `json:"classes"`
	Reasons         map[string]int    `json:"reasons"`
	EstimateReasons map[string]int    `json:"estimate_statuses,omitempty"`
	Samples         []Sample          `json:"samples,omitempty"`
	RecordedBytes   int64             `json:"recorded_row_bytes"`
	LogicalBytes    int64             `json:"unique_logical_bytes"`
	AllocatedBytes  int64             `json:"unique_allocated_bytes"`
}

type Tally struct {
	Count          int   `json:"count"`
	ForegroundUsed int   `json:"foreground_used"`
	RecordedBytes  int64 `json:"recorded_row_bytes"`
	LogicalBytes   int64 `json:"unique_logical_bytes"`
	AllocatedBytes int64 `json:"unique_allocated_bytes"`
}

type Sample struct {
	ID                 string     `json:"id,omitempty"`
	Class              string     `json:"class"`
	Reason             string     `json:"reason"`
	Estimate           string     `json:"estimate,omitempty"`
	EvidenceAt         *time.Time `json:"evidence_at,omitempty"`
	ForegroundUsed     bool       `json:"foreground_used"`
	HasLedgerReference bool       `json:"has_ledger_reference"`
}

type inode struct{ dev, ino uint64 }

type totals struct {
	all     map[inode]bool
	byClass map[string]map[inode]bool
}

func newSection() Section {
	return Section{Classes: make(map[string]*Tally), Reasons: make(map[string]int)}
}

func (t *totals) add(s *Section, sample Sample, limit int, recorded int64, stat unix.Stat_t) {
	s.Reasons[sample.Reason]++
	if sample.Estimate != "" {
		if s.EstimateReasons == nil {
			s.EstimateReasons = make(map[string]int)
		}
		s.EstimateReasons[sample.Estimate]++
	}
	c := s.Classes[sample.Class]
	if c == nil {
		c = &Tally{}
		s.Classes[sample.Class] = c
	}
	c.Count++
	if sample.ForegroundUsed {
		c.ForegroundUsed++
	}
	c.RecordedBytes += recorded
	s.RecordedBytes += recorded
	if len(s.Samples) < limit {
		s.Samples = append(s.Samples, sample)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return
	}
	key := inode{uint64(stat.Dev), stat.Ino}
	bytes := allocated(stat)
	if t.all == nil {
		t.all = make(map[inode]bool)
		t.byClass = make(map[string]map[inode]bool)
	}
	if !t.all[key] {
		t.all[key] = true
		s.LogicalBytes += stat.Size
		s.AllocatedBytes += bytes
	}
	set := t.byClass[sample.Class]
	if set == nil {
		set = make(map[inode]bool)
		t.byClass[sample.Class] = set
	}
	if !set[key] {
		set[key] = true
		c.LogicalBytes += stat.Size
		c.AllocatedBytes += bytes
	}
}

func allocated(stat unix.Stat_t) int64 { return stat.Blocks * 512 }

type classified struct {
	publication
	relative    string
	stat        unix.Stat_t
	sample      Sample
	eligible    bool
	physicalGap bool
}

func classify(p publication, root int, moves map[string]bool, moveCoverage bool, ev evidence, now time.Time) classified {
	c := classified{publication: p, sample: Sample{
		ID: p.id(), Class: "uncertain", ForegroundUsed: p.used > 0, HasLedgerReference: p.hasRef,
	}}
	if p.key == "" || p.size <= 0 || p.asset == "" || p.variant == "" || !lowerHex(p.digest, 64) {
		c.sample.Reason = "invalid_ledger_record"
		return c
	}
	c.relative = diskcache.RelativePath(p.key)
	stat, reason := fileInfo(root, c.relative)
	if reason != "" {
		c.sample.Reason = reason
		if reason == "missing" {
			c.sample.Class = "file_missing"
		} else {
			c.physicalGap = true
		}
		return c
	}
	c.stat = stat
	if stat.Size != p.size {
		c.sample.Class, c.sample.Reason = "size_mismatch", "recorded_size_differs"
		return c
	}
	switch {
	case moves[p.asset]:
		c.sample.Reason = "move_pending_or_ambiguous"
	case !moveCoverage:
		c.sample.Reason = "move_coverage_incomplete"
	case !keyMatchesPublication(p):
		c.sample.Reason = "unsupported_publication_key"
	default:
		obs := ev.Publications[c.sample.ID]
		switch obs.State {
		case "version_current":
			if obs.Reference.Asset == p.asset && obs.current && obs.At.UnixNano() >= p.generated {
				c.sample.Class, c.sample.Reason = "valid_referenced", "fixture_exact_reference_and_current_version"
				c.sample.EvidenceAt = &obs.At
			} else {
				c.sample.Reason = "fixture_reference_or_time_mismatch"
			}
		case "version_retired", "no_reference":
			if p.hasRef || obs.At.UnixNano() < p.generated {
				c.sample.Reason = "reference_present_or_evidence_predates_publication"
				break
			}
			c.sample.Class, c.sample.Reason = obs.State, "fixture_complete_scope"
			c.sample.EvidenceAt = &obs.At
			c.sample.Estimate = "grace_unelapsed"
			if now.Sub(obs.At) >= fixtureGrace {
				c.eligible = true
			}
		case "":
			c.sample.Reason = "no_retirement_or_reference_coverage"
			if p.hasRef {
				c.sample.Reason = "location_not_resolved"
			}
		default:
			c.sample.Reason = obs.State
			c.sample.EvidenceAt = &obs.At
		}
	}
	return c
}

func Run(parent context.Context, opt Options) (Report, error) {
	now := time.Now().UTC()
	report := Report{StartedAt: now, EvidenceScope: "ledger_only", Ledger: LedgerSection{Section: newSection()}, Inventory: newSection()}
	if opt.CacheDir == "" || opt.Snapshot == "" || opt.AfterResult < 0 ||
		((opt.AfterResult > 0 || opt.AfterCache != "") && !lowerHex(opt.ExpectSnapshot, 64)) ||
		len(opt.AfterCache) > 1500 || opt.MaxEntries < 1 || opt.MaxEntries > 20000 ||
		opt.MaxBytes < 1 || opt.MaxBytes > 1<<40 || opt.MaxTime < time.Millisecond ||
		opt.MaxTime > 30*time.Second || opt.Samples < 0 || opt.Samples > 50 {
		return report, errors.New("invalid inputs or limits; see -help")
	}
	report.Limits = OptionsReport{opt.MaxEntries, opt.MaxBytes, opt.MaxTime.Milliseconds(), opt.Samples}
	ctx, cancel := context.WithTimeout(parent, opt.MaxTime)
	defer cancel()
	report.Ledger.AfterRow, report.Ledger.NextRow = opt.AfterResult, opt.AfterResult
	report.Inventory.NextCache = opt.AfterCache

	db, snapshotID, unchanged, err := openSnapshot(opt.Snapshot)
	if err != nil {
		return report, err
	}
	defer db.Close()
	report.SnapshotID = snapshotID
	if opt.ExpectSnapshot != "" && opt.ExpectSnapshot != snapshotID {
		return report, errors.New("snapshot identity changed; restart trial")
	}
	var schema int
	if err := db.QueryRowContext(ctx, "SELECT version FROM ledger_schema LIMIT 1").Scan(&schema); err != nil || schema != 1 {
		if ctx.Err() != nil {
			finishReport(&report, parent, ctx)
			return report, nil
		}
		return report, errors.New("cannot read a version-1 cache ledger snapshot")
	}
	root, err := openDisk(opt.CacheDir)
	if err != nil {
		return report, fmt.Errorf("cannot open dedicated diskcache: %s", diskReason(err))
	}
	defer unix.Close(root)
	w, err := newWalker(root, opt.AfterCache)
	if err != nil {
		return report, err
	}
	defer w.close()

	var fs unix.Statfs_t
	if unix.Fstatfs(root, &fs) == nil {
		unit := fs.Frsize
		if unit <= 0 {
			unit = fs.Bsize
		}
		available := uint64(fs.Bavail) * uint64(unit)
		report.PartitionAvailable = &available
	}
	ev, err := readEvidence(ctx, db, opt.FixtureEvidence, now)
	if err != nil && ctx.Err() == nil {
		return report, err
	}
	if ev.Scope != "" {
		report.EvidenceScope = "synthetic_fixture"
	}
	moves, complete, err := readMoves(ctx, db, opt.MaxEntries)
	if err != nil && ctx.Err() == nil {
		return report, errors.New("cannot read outstanding moves")
	}
	report.MovesComplete = complete
	batch, err := scanResults(ctx, db, opt, &report.Ledger, root, moves, complete, ev, now)
	if err != nil {
		return report, err
	}
	registered := summarizeResults(ctx, opt, &report, root, batch)
	scanInventory(ctx, opt, &report.Inventory, w, registered, report.Ledger.ResultKeyCoverage, ev)
	if !unchanged() {
		return report, errors.New("ledger snapshot changed; discard the report")
	}
	finishReport(&report, parent, ctx)
	return report, nil
}

func finishReport(report *Report, parent, ctx context.Context) {
	report.FinishedAt = time.Now().UTC()
	switch {
	case parent.Err() != nil:
		report.Stop = "canceled"
	case ctx.Err() != nil:
		report.Stop = "time_limit"
	case !report.Ledger.Complete || !report.Inventory.Complete || !report.MovesComplete || report.Inventory.Gaps || report.Ledger.Gaps:
		report.Stop = "partial"
	}
}

func scanResults(ctx context.Context, db *sql.DB, opt Options, section *LedgerSection, root int,
	moves map[string]bool, moveCoverage bool, ev evidence, now time.Time) ([]classified, error) {
	section.AfterRow, section.NextRow = opt.AfterResult, opt.AfterResult
	if ctx.Err() != nil {
		section.Stop = "interrupted"
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, resultPage, opt.AfterResult, opt.MaxEntries+1)
	if err != nil {
		if ctx.Err() != nil {
			section.Stop = "interrupted"
			return nil, nil
		}
		return nil, errors.New("cannot read result page")
	}
	defer rows.Close()
	var batch []classified
	var recorded, current int64
	var resultKeyGap bool
	for rows.Next() {
		if len(batch) == opt.MaxEntries {
			section.Stop = "entry_limit"
			break
		}
		if ctx.Err() != nil {
			break
		}
		var p publication
		if err := rows.Scan(&p.row, &p.asset, &p.variant, &p.key, &p.size, &p.digest, &p.generated, &p.used, &p.hasRef); err != nil {
			return nil, errors.New("cannot read publication")
		}
		c := classify(p, root, moves, moveCoverage, ev, now)
		if p.size > opt.MaxBytes || c.stat.Size > opt.MaxBytes {
			c.sample.Class, c.sample.Reason, c.sample.Estimate = "skipped", "object_exceeds_byte_limit", ""
			c.eligible, c.stat, c.size = false, unix.Stat_t{}, 0
			resultKeyGap = true
		} else if p.size > opt.MaxBytes-recorded || c.stat.Size > opt.MaxBytes-current {
			section.Stop = "byte_limit"
			break
		}
		resultKeyGap = resultKeyGap || c.sample.Reason == "invalid_ledger_record"
		section.Gaps = section.Gaps || resultKeyGap || c.physicalGap
		batch = append(batch, c)
		recorded += c.size
		current += c.stat.Size
		section.NextRow = p.row
	}
	if ctx.Err() != nil {
		section.Stop = "interrupted"
	} else if rows.Err() != nil {
		return nil, errors.New("result page read failed")
	}
	section.Visited = len(batch)
	section.Complete = section.Stop == ""
	section.ResultKeyCoverage = section.Complete && !resultKeyGap && opt.AfterResult == 0
	section.Coverage = section.ResultKeyCoverage && !section.Gaps
	return batch, nil
}

func summarizeResults(ctx context.Context, opt Options, report *Report, root int, batch []classified) map[string]bool {
	groups := make(map[string][]int)
	registered := make(map[string]bool)
	var keys []string
	for i, c := range batch {
		if c.relative != "" {
			if !registered[c.relative] {
				keys = append(keys, c.relative)
			}
			groups[c.relative] = append(groups[c.relative], i)
			registered[c.relative] = true
		}
	}

	estimated := make(map[inode]bool)
	for _, relative := range keys {
		group := groups[relative]
		first := batch[group[0]]
		reason := "fixture_estimate"
		switch {
		case !report.Ledger.ResultKeyCoverage:
			reason = "result_coverage_incomplete"
		case first.stat.Nlink != 1:
			reason = "hardlink_or_unavailable"
		default:
			for _, i := range group {
				c := batch[i]
				if !c.eligible || c.digest != first.digest || !sameFileVersion(c.stat, first.stat) {
					reason = "shared_object_protected"
					break
				}
			}
		}
		if reason == "fixture_estimate" {
			if !verifyFixtureBytes(ctx, root, relative, first.stat, first.digest) {
				reason = "publication_bytes_unverified"
			} else {
				id := inode{uint64(first.stat.Dev), first.stat.Ino}
				if !estimated[id] {
					estimated[id] = true
					report.EstimatedCandidates++
					report.EstimatedLogical += first.stat.Size
					report.EstimatedAllocated += allocated(first.stat)
				}
			}
		}
		for _, i := range group {
			if batch[i].eligible {
				batch[i].sample.Estimate = reason
			}
		}
	}
	var counted totals
	for _, c := range batch {
		counted.add(&report.Ledger.Section, c.sample, opt.Samples, c.size, c.stat)
	}
	return registered
}

func scanInventory(ctx context.Context, opt Options, section *Section, w *walker,
	registered map[string]bool, registryComplete bool, ev evidence) {
	var counted totals
	var size int64
	cursor := w.cursor()
	for section.Visited < opt.MaxEntries {
		entry, err := w.next(ctx)
		if err == io.EOF {
			section.Complete = true
			section.NextCache = "done"
			section.Coverage = opt.AfterCache == "" && !section.Gaps
			return
		}
		if err != nil {
			section.Stop = "interrupted"
			break
		}
		sample := Sample{Class: "skipped", Reason: entry.reason}
		if entry.relative != "" {
			sample.ID = anonymous(entry.relative)
		}
		switch {
		case entry.reason != "":
			section.Gaps = section.Gaps || entry.reason != "outside_hash_layout"
		case entry.stat.Size > opt.MaxBytes:
			sample.Reason, entry.stat, section.Gaps = "object_exceeds_byte_limit", unix.Stat_t{}, true
		case entry.stat.Size > opt.MaxBytes-size:
			section.Stop, section.NextCache = "byte_limit", cursor
			return
		case entry.kind == "shard":
			sample.Class, sample.Reason = "shard", "directory"
		case entry.kind == "temporary":
			sample.Class, sample.Reason = "temporary", "atomic_store_temporary"
		case registered[entry.relative]:
			sample.Class, sample.Reason = "registered", "physical_key_in_ledger"
		case !registryComplete:
			sample.Class, sample.Reason = "unattributed", "ledger_coverage_incomplete"
		case slices.Contains(ev.Unregistered, sample.ID):
			sample.Class, sample.Reason = "unregistered_publication", "fixture_observed_unregistered_write"
		default:
			sample.Class, sample.Reason = "unattributed_hash", "legacy_or_unregistered_publication"
		}
		size += entry.stat.Size
		section.Visited++
		counted.add(section, sample, opt.Samples, 0, entry.stat)
		cursor = w.cursor()
	}
	if section.Stop == "" {
		section.Stop = "entry_limit"
	}
	section.NextCache = cursor
}
