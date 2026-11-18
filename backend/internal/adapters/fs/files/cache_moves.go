// Added for mument 007-C. See docs/development/007c/DESIGN.md.
package files

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/fileutils"
	"github.com/gtsteffaniak/filebrowser/backend/internal/cacheledger"
	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/go-logger/logger"
)

const MoveGroupLimit = 64

type ResourceMove struct {
	Directory      bool
	Source, Target string
	From, To       string
}

type observedMove struct {
	src, dst         *indexing.Index
	info             os.FileInfo
	after            os.FileInfo
	copy             bool
	directoryVersion cacheledger.Version
	outcome          cacheledger.RenameOutcome
}

// MoveResourcesBatched takes a bounded group of independently authorized moves.
// Callers flush dependencies before submitting a later operation.
func MoveResourcesBatched(batch *IndexImpactBatch, moves []ResourceMove) []error {
	return svc().moveResources(batch, moves)
}

func (s *Service) moveResources(batch *IndexImpactBatch, moves []ResourceMove) []error {
	results := make([]error, len(moves))
	if len(moves) > MoveGroupLimit {
		for i := range results {
			results[i] = fmt.Errorf("move group exceeds limit")
		}
		return results
	}
	observations := make([]observedMove, len(moves))
	for i, m := range moves {
		if m.From == m.To {
			results[i] = fmt.Errorf("cannot move a file to itself: %s", m.From)
			continue
		}
		if err := validateMoveDestination(m.From, m.To, m.Directory); err != nil {
			results[i] = err
			continue
		}
		src, dst := indexing.GetIndex(m.Source), indexing.GetIndex(m.Target)
		if src == nil {
			results[i] = fmt.Errorf("could not get source index: %v", m.Source)
			continue
		}
		if dst == nil {
			results[i] = fmt.Errorf("could not get destination index: %v", m.Target)
			continue
		}
		observations[i].src, observations[i].dst = src, dst
	}
	s.renameResources(moves, observations, results)
	for i, m := range moves {
		if results[i] != nil {
			continue
		}
		obs := &observations[i]
		if obs.copy {
			results[i] = fileutils.MoveByCopy(m.From, m.To)
			if results[i] != nil {
				continue
			}
		}
		s.finishMove(batch, obs.src, obs.dst, m.Directory, m.From, m.To, obs.directoryVersion)
	}
	return results
}

func (s *Service) renameResources(moves []ResourceMove, observations []observedMove, results []error) {
	repo := preview.MoveLedger()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if repo != nil {
		unlock, err := repo.LockLocations(ctx)
		if err != nil {
			logger.Warningf("preview ledger contention during move: %v", err)
			repo = nil
		} else {
			defer unlock()
		}
	}

	if repo != nil {
		prepareCacheMoves(ctx, repo, moves, observations, results)
	}
	var outcomes []cacheledger.RenameOutcome
	for i, m := range moves {
		if results[i] != nil {
			continue
		}
		obs := &observations[i]
		before, err := os.Lstat(m.From)
		if err == nil && before.Mode().IsRegular() {
			if obs.info != nil && cacheledger.FromFileInfo(obs.info) != cacheledger.FromFileInfo(before) {
				obs.info = nil // changed after intent; proceed with ordinary filesystem semantics
			} else {
				obs.info = before
			}
		} else {
			obs.info = nil
		}
		started := time.Now()
		err = os.Rename(m.From, m.To)
		if repo != nil {
			repo.RecordRename(time.Since(started))
		}
		if err != nil {
			obs.copy = true
		} else if m.Directory && before != nil && before.IsDir() {
			after, err := os.Lstat(m.To)
			_, sourceErr := os.Lstat(m.From)
			if err == nil && errors.Is(sourceErr, os.ErrNotExist) && after.IsDir() {
				v := cacheledger.FromFileInfo(after)
				if cacheledger.FromFileInfo(before).RenameCompatible(v) {
					obs.directoryVersion = v
				}
			}
		} else if obs.info != nil {
			after, err := os.Lstat(m.To)
			_, sourceErr := os.Lstat(m.From)
			if err == nil && errors.Is(sourceErr, os.ErrNotExist) && after.Mode().IsRegular() {
				version := cacheledger.FromFileInfo(after)
				if cacheledger.FromFileInfo(obs.info).RenameCompatible(version) {
					obs.after = after
					obs.outcome.After = version
				}
			}
		}
		if obs.outcome.ID != "" {
			outcomes = append(outcomes, obs.outcome)
		}
	}
	if repo != nil {
		if err := repo.CompleteRenames(ctx, outcomes); err != nil {
			logger.Warningf("filesystem move completed with uncommitted preview ledger intents: %v", err)
			repo.RequestRecovery()
		}
		// Finish B transfer before another application rename acquires the location
		// lock. It checks the current target and executes only keyed SQL statements.
		for i, m := range moves {
			obs := &observations[i]
			if obs.after != nil {
				indexing.TransferMovedImage(obs.src, obs.dst, m.From, m.To, obs.info, obs.after)
			}
		}
	}
}

func prepareCacheMoves(ctx context.Context, repo *cacheledger.Repository, moves []ResourceMove, observations []observedMove, results []error) {
	var intents []cacheledger.Move
	var slots []int
	for i, m := range moves {
		if results[i] != nil || m.Directory {
			continue
		}
		obs := &observations[i]
		info, err := os.Lstat(m.From)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		obs.info = info
		v := cacheledger.FromFileInfo(info)
		src, name, ok := cacheledger.Location(obs.src.Path, m.From)
		if !ok {
			continue
		}
		dst, target, ok := cacheledger.Location(obs.dst.Path, m.To)
		if !ok || (src == dst && name == target) {
			continue
		}
		ref, found, err := repo.Existing(ctx, src, name, v)
		if err != nil {
			logger.Warningf("cannot prepare preview ledger move: %v", err)
			return
		}
		if !found {
			continue
		}
		intents = append(intents, cacheledger.Move{Ref: ref, TargetSource: dst, TargetPath: target})
		slots = append(slots, i)
	}
	if len(intents) == 0 {
		return
	}
	persisted, err := repo.Prepare(ctx, intents)
	if err != nil {
		logger.Warningf("cannot persist preview ledger move batch: %v", err)
		return
	}
	for i, intent := range persisted {
		observations[slots[i]].outcome.ID = intent.ID
	}
}
