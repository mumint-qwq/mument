package preview

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/diskcache"
	"github.com/gtsteffaniak/filebrowser/backend/internal/cacheledger"
	"github.com/gtsteffaniak/go-logger/logger"
)

type orphanSourceProof struct {
	Source string
	diskcache.SourceProof
}

// OrphanMutation keeps a descriptor to the old inode until the file adapter
// can prove the operation actually invalidated that exact reference.
type OrphanMutation struct {
	repo  *cacheledger.Repository
	old   *diskcache.SourceObservation
	id    string
	grace time.Duration
}

func OrphanTracking() *cacheledger.Repository {
	if service == nil || service.closed.Load() || service.orphans == nil || service.orphans.tree.Check(false) != nil {
		return nil
	}
	return service.ledger
}

// BeginOrphanMutation requires the C location gate. Failure to observe or stage
// evidence leaves the normal file operation usable and the preview protected.
func BeginOrphanMutation(ctx context.Context, repo *cacheledger.Repository, source, root, path, reason string) *OrphanMutation {
	return beginOrphanMutation(ctx, repo, source, root, path, reason, nil)
}

func beginOrphanMutation(ctx context.Context, repo *cacheledger.Repository, source, root, path, reason string, inventory *diskcache.SourceDirectory) *OrphanMutation {
	if repo == nil || repo != OrphanTracking() {
		return nil
	}
	old, err := diskcache.ObserveSource(root, path)
	if err != nil {
		return nil
	}
	keep := false
	defer func() {
		if !keep {
			old.Close()
		}
	}()
	v := cacheledger.FromFileInfo(old.Info)
	ns, name, ok := cacheledger.Location(root, path)
	if !ok {
		return nil
	}
	ref, found, err := repo.Existing(ctx, ns, name, v)
	if err != nil || !found {
		return nil
	}
	sourceProof := old.Proof
	if inventory != nil {
		if sourceProof.RootID != inventory.Proof.RootID || !inventory.Contains(path, old.Info) {
			return nil
		}
		sourceProof = inventory.Proof
	}
	proof, _ := json.Marshal(orphanSourceProof{source, sourceProof})
	id, err := repo.StageInvalidation(ctx, cacheledger.Invalidation{Ref: ref, Reason: reason, Proof: string(proof)}, time.Now())
	if err != nil {
		logger.Warningf("could not stage preview invalidation")
		return nil
	}
	keep = true
	return &OrphanMutation{repo: repo, old: old, id: id, grace: service.orphans.grace}
}

func (m *OrphanMutation) ProvesUnlinked(replaced bool) bool {
	return m != nil && m.old.Unlinked(replaced)
}

func (m *OrphanMutation) MatchesOld(info os.FileInfo) bool {
	return m != nil && info != nil && cacheledger.FromFileInfo(m.old.Info) == cacheledger.FromFileInfo(info)
}

func (m *OrphanMutation) FinishOverwrite(written os.FileInfo) {
	if m == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if written == nil {
		m.Finish(ctx, false, "")
		return
	}
	unlock, err := m.repo.LockLocations(ctx)
	if err != nil {
		defer m.old.Close()
		logger.Warningf("preview invalidation awaiting reconciliation")
		return
	}
	defer unlock()
	proven := !m.MatchesOld(written) && m.old.Updated(written)
	m.Finish(ctx, proven, "")
}

func (m *OrphanMutation) Finish(ctx context.Context, proven bool, move string) {
	if m == nil {
		return
	}
	defer m.old.Close()
	if proven {
		if err := m.repo.ConfirmInvalidation(ctx, m.id, move, time.Now(), m.grace); err != nil {
			logger.Warningf("preview invalidation awaiting reconciliation")
		}
		return
	}
	if err := m.repo.AbortInvalidation(ctx, m.id); err != nil {
		logger.Warningf("preview invalidation awaiting reconciliation")
	}
}

// OrphanDirectory groups only entries whose pre-operation identities fit in
// one small, complete same-mount inventory.
type OrphanDirectory struct {
	directory  *diskcache.SourceDirectory
	mutations  []*OrphanMutation
	incomplete bool
}

// BeginOrphanDirectory takes ownership of the already bounded inventory. It
// serializes staging with C, then releases the gate before recursive removal.
func BeginOrphanDirectory(ctx context.Context, repo *cacheledger.Repository, source, root string, directory *diskcache.SourceDirectory) *OrphanDirectory {
	unlock, err := repo.LockLocations(ctx)
	if err != nil {
		directory.Close()
		return nil
	}
	defer unlock()

	group := &OrphanDirectory{directory: directory}
	for _, child := range directory.Files {
		if ctx.Err() != nil {
			break
		}
		if m := beginOrphanMutation(ctx, repo, source, root, child, "directory_delete", directory); m != nil {
			group.mutations = append(group.mutations, m)
		}
	}
	group.incomplete = ctx.Err() != nil
	return group
}

func (d *OrphanDirectory) Finish(successful bool) {
	if d == nil {
		return
	}
	defer d.directory.Close()
	if len(d.mutations) == 0 {
		return
	}

	removed := successful && !d.incomplete && d.directory.Removed()
	proven := make([]bool, len(d.mutations))
	for i, m := range d.mutations {
		proven[i] = removed && m.old.LostLink()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	unlock, err := d.mutations[0].repo.LockLocations(ctx)
	if err != nil {
		for _, m := range d.mutations {
			m.old.Close()
		}
		logger.Warningf("preview directory invalidations awaiting reconciliation")
		return
	}
	defer unlock()
	for i, m := range d.mutations {
		m.Finish(ctx, proven[i], "")
	}
}
