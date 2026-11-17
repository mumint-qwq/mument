// Ported from the FileBrowser Quantum 007-A RC4 / 007-B RC3 candidates.
// See docs/development/007-ab-migration.md for provenance and validation.
package indexing

import (
	"context"
	"os"
	"sync/atomic"
	"time"

	dbsql "github.com/gtsteffaniak/filebrowser/backend/internal/database/sql"
	"github.com/gtsteffaniak/filebrowser/backend/internal/utils"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/go-logger/logger"
)

const imageCandidateBatchSize = 64

type imageCandidate struct {
	path    string
	version fileVersion
	obsSeq  uint64
}

// One header read at a time across every source. This is deliberately a
// package-level semaphore: per-source semaphores would let three disks run
// three parsers concurrently.
var imageHeaderSem = make(chan struct{}, 1)

type imageMetadataCounters struct {
	candidates  atomic.Int64
	ready       atomic.Int64
	deferred    atomic.Int64
	unsupported atomic.Int64
	failed      atomic.Int64
	stale       atomic.Int64
	rejected    atomic.Int64
	dropped     atomic.Int64
	headerBytes atomic.Int64
}

var imageMetaCounters imageMetadataCounters

// ImageMetadataStats is a point-in-time aggregate for diagnostics and tests.
type ImageMetadataStats struct {
	Candidates  int64
	Ready       int64
	Deferred    int64
	Unsupported int64
	Failed      int64
	Stale       int64
	Rejected    int64
	Dropped     int64
	HeaderBytes int64
}

func ImageMetadataStatsSnapshot() ImageMetadataStats {
	return ImageMetadataStats{
		Candidates:  imageMetaCounters.candidates.Load(),
		Ready:       imageMetaCounters.ready.Load(),
		Deferred:    imageMetaCounters.deferred.Load(),
		Unsupported: imageMetaCounters.unsupported.Load(),
		Failed:      imageMetaCounters.failed.Load(),
		Stale:       imageMetaCounters.stale.Load(),
		Rejected:    imageMetaCounters.rejected.Load(),
		Dropped:     imageMetaCounters.dropped.Load(),
		HeaderBytes: imageMetaCounters.headerBytes.Load(),
	}
}

func ResetImageMetadataStatsForTesting() {
	imageMetaCounters.candidates.Store(0)
	imageMetaCounters.ready.Store(0)
	imageMetaCounters.deferred.Store(0)
	imageMetaCounters.unsupported.Store(0)
	imageMetaCounters.failed.Store(0)
	imageMetaCounters.stale.Store(0)
	imageMetaCounters.rejected.Store(0)
	imageMetaCounters.dropped.Store(0)
	imageMetaCounters.headerBytes.Store(0)
}

func (s *Scanner) queueImageCandidate(path string, version fileVersion, obsSeq uint64) {
	if s == nil || s.idx == nil {
		return
	}
	if len(s.imageCandidates) >= imageCandidateBatchSize {
		// The row is already observable as unknown; the bounded backfill query
		// will pick it up. Do not grow a per-directory path queue.
		imageMetaCounters.dropped.Add(1)
		return
	}
	s.imageCandidates = append(s.imageCandidates, imageCandidate{path: path, version: version, obsSeq: obsSeq})
	imageMetaCounters.candidates.Add(1)
}

func (s *Scanner) flushImageCandidates() {
	if s == nil || len(s.imageCandidates) == 0 {
		return
	}
	candidates := s.imageCandidates
	s.imageCandidates = nil

	// The candidate rows must exist before UPDATE-only commits can match them.
	s.flushBatch()

	paths := make([]string, 0, len(candidates))
	for _, c := range candidates {
		paths = append(paths, c.path)
	}
	works, err := s.idx.db.GetImageWorksByPaths(s.idx.Name, paths)
	if err != nil {
		logger.Warningf("[%s] image metadata candidate lookup failed: %v", s.idx.Name, err)
		imageMetaCounters.rejected.Add(int64(len(candidates)))
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-s.idx.schedulerStop:
			cancel()
		case <-ctx.Done():
		}
	}()
	for _, c := range candidates {
		if s.idx.schedulerStopped() {
			return
		}
		work, ok := works[c.path]
		expected := c.version.String()
		if !ok || work.Version != expected || work.ObsSeq != c.obsSeq {
			imageMetaCounters.rejected.Add(1)
			continue
		}
		s.idx.processImageWork(ctx, work, c.version)
	}
}

func (idx *Index) processImageWork(ctx context.Context, work dbsql.ImageWork, expected fileVersion) bool {
	result := readImageHeader(ctx, utils.JoinUnderSourceRoot(idx.Path, work.Path), expected)
	imageMetaCounters.headerBytes.Add(result.BytesRead)

	switch result.State {
	case iteminfo.ImageMetaReady:
		imageMetaCounters.ready.Add(1)
	case iteminfo.ImageMetaDeferred:
		imageMetaCounters.deferred.Add(1)
	case iteminfo.ImageMetaUnsupported:
		imageMetaCounters.unsupported.Add(1)
	default:
		imageMetaCounters.failed.Add(1)
	}
	if result.Err == imageMetaErrStale {
		imageMetaCounters.stale.Add(1)
	}

	commit := dbsql.ImageMetadataCommit{
		State:       result.State,
		Width:       result.Width,
		Height:      result.Height,
		Orientation: result.Orientation,
		Ratio:       result.Ratio,
	}
	if result.State == iteminfo.ImageMetaDeferred || result.State == iteminfo.ImageMetaFailed {
		count := work.RetryCount
		if count < 65535 {
			count++
		}
		commit.RetryCount = count
		commit.RetryAt = time.Now().Add(retryDelay(result.State, count)).Unix()
	}
	ok, err := idx.db.CommitImageMetadata(idx.Name, work.Path, work.Version, work.Guard, commit)
	if err != nil {
		logger.Warningf("[%s] image metadata commit failed: %v", idx.Name, err)
		return false
	}
	if !ok {
		imageMetaCounters.rejected.Add(1)
	}
	return ok
}

func retryDelay(state iteminfo.ImageMetaState, count uint16) time.Duration {
	switch state {
	case iteminfo.ImageMetaDeferred:
		d := time.Minute * time.Duration(count+1)
		if d > time.Hour {
			d = time.Hour
		}
		return d
	case iteminfo.ImageMetaFailed:
		shift := count
		if shift > 8 {
			shift = 8
		}
		d := 5 * time.Minute * time.Duration(1<<shift)
		if d > 24*time.Hour {
			d = 24 * time.Hour
		}
		return d
	default:
		return 0
	}
}

func (idx *Index) schedulerStopped() bool {
	if idx == nil || idx.schedulerStop == nil {
		return false
	}
	select {
	case <-idx.schedulerStop:
		return true
	default:
		return false
	}
}

func (idx *Index) runImageMetadataBackfill() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-idx.schedulerStop:
			cancel()
		case <-ctx.Done():
		}
	}()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		processed := idx.backfillImageMetadataBatch(ctx)
		if processed == 0 {
			select {
			case <-idx.schedulerStop:
				return
			case <-ticker.C:
			}
			continue
		}
		// A full batch likely means more work is waiting; give other scanners a
		// brief turn and continue without building an unbounded queue.
		select {
		case <-idx.schedulerStop:
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (idx *Index) backfillImageMetadataBatch(ctx context.Context) int {
	now := time.Now().Unix()
	works, err := idx.db.GetImageMetadataCandidates(idx.Name, "", imageCandidateBatchSize, now)
	if err != nil {
		logger.Warningf("[%s] image metadata backfill query failed: %v", idx.Name, err)
		return 0
	}
	handled := 0
	for _, work := range works {
		if idx.schedulerStopped() || ctx.Err() != nil {
			break
		}
		realPath := utils.JoinUnderSourceRoot(idx.Path, work.Path)
		info, statErr := os.Stat(realPath)
		if statErr != nil {
			// Do not let missing/offline rows occupy the first page forever.
			retryAt := time.Now().Add(retryDelay(iteminfo.ImageMetaDeferred, work.RetryCount+1)).Unix()
			if ok, _ := idx.db.DeferImageWork(idx.Name, work.Path, work.Version, work.Guard, retryAt); ok {
				handled++
			}
			continue
		}
		expected := versionFromFileInfo(info, realPath)
		if expected.String() != work.Version {
			// The filesystem has moved ahead. Invalidate the old projection and
			// back off; the scanner owns the new version observation.
			retryAt := time.Now().Add(retryDelay(iteminfo.ImageMetaDeferred, work.RetryCount+1)).Unix()
			if ok, _ := idx.db.DeferImageWork(idx.Name, work.Path, work.Version, work.Guard, retryAt); ok {
				handled++
			}
			continue
		}
		if idx.processImageWork(ctx, work, expected) {
			handled++
		}
	}
	return handled
}
