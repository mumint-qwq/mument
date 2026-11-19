package files

import (
	"path/filepath"
	"sync"

	"github.com/gtsteffaniak/filebrowser/backend/internal/cacheledger"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/go-logger/logger"
)

// IndexImpactBatch collects the index maintenance produced by one HTTP request.
//
// The batch does not scan anything: it only records which directories are
// affected. A 267-file move within one source therefore collects two directory
// intents (source parent and destination parent), not 534 refresh requests.
// Submitting the batch hands those intents to the process-wide maintenance
// queue, which merges them with work from other requests.
type IndexImpactBatch struct {
	mu      sync.Mutex
	order   []refreshKey
	impacts map[refreshKey]*DirectoryRefresh
}

// NewIndexImpactBatch returns an empty request-scoped collector.
func NewIndexImpactBatch() *IndexImpactBatch {
	return &IndexImpactBatch{impacts: make(map[refreshKey]*DirectoryRefresh)}
}

// Add merges intents, keeping one entry per (source, directory). Deletions are
// unioned, recursive requirements are strengthened, deep indexes are deduped.
func (b *IndexImpactBatch) Add(impacts ...DirectoryRefresh) {
	if b == nil || len(impacts) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, impact := range impacts {
		dir := normalizeRefreshDirKey(impact.Dir)
		if impact.Source == "" || dir == "" {
			continue
		}
		impact.Dir = dir
		key := refreshKey{source: impact.Source, dir: dir}
		if existing, ok := b.impacts[key]; ok {
			mergeDirectoryRefresh(existing, impact)
			continue
		}
		copied := impact
		copied.Deep = append([]string(nil), impact.Deep...)
		copied.Deletes = append([]PendingDelete(nil), impact.Deletes...)
		b.impacts[key] = &copied
		b.order = append(b.order, key)
	}
}

// Len reports how many distinct directory intents the batch holds.
func (b *IndexImpactBatch) Len() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.impacts)
}

// Impacts returns a copy of the collected intents in insertion order.
func (b *IndexImpactBatch) Impacts() []DirectoryRefresh {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]DirectoryRefresh, 0, len(b.order))
	for _, key := range b.order {
		impact := b.impacts[key]
		if impact == nil {
			continue
		}
		copied := *impact
		copied.Deep = append([]string(nil), impact.Deep...)
		copied.Deletes = append([]PendingDelete(nil), impact.Deletes...)
		out = append(out, copied)
	}
	return out
}

// Submit hands the collected intents to the maintenance queue. It is safe to
// call on a nil batch. The HTTP handler defers it so partial success is still
// applied when the request later fails or the client disconnects.
func (b *IndexImpactBatch) Submit() {
	if b == nil {
		return
	}
	impacts := b.Impacts()
	if len(impacts) == 0 {
		return
	}
	if err := SubmitIndexMaintenance(impacts); err != nil {
		logger.Errorf("could not submit index maintenance for a file operation batch: %v", err)
	}
}

func mergeDirectoryRefresh(dst *DirectoryRefresh, src DirectoryRefresh) {
	if src.Refresh {
		dst.Refresh = true
	}
	if src.Recursive {
		dst.Recursive = true
	}
	for _, deep := range src.Deep {
		if deep == "" || containsString(dst.Deep, deep) {
			continue
		}
		dst.Deep = append(dst.Deep, deep)
	}
	for _, deletion := range src.Deletes {
		if deletion.Path == "" {
			continue
		}
		merged := false
		for i := range dst.Deletes {
			if dst.Deletes[i].Path == deletion.Path && dst.Deletes[i].IsDir == deletion.IsDir {
				dst.Deletes[i].Recursive = dst.Deletes[i].Recursive || deletion.Recursive
				dst.Deletes[i].imageTree = deletion.imageTree
				merged = true
				break
			}
		}
		if !merged {
			dst.Deletes = append(dst.Deletes, deletion)
		}
	}
}

// moveIndexImpacts describes the index work a successful move still needs. It
// is built only after the physical move succeeded, from the same source and
// destination identities the move used.
func moveIndexImpacts(srcIdx, dstIdx *indexing.Index, isSrcDir bool, realsrc, realdst string, directoryVersion cacheledger.Version) []DirectoryRefresh {
	impacts := make([]DirectoryRefresh, 0, 2)
	if !srcIdx.Config.ResolvedRules.IndexingDisabled {
		var imageTree *movedImageTree
		if directoryVersion.Strong && !dstIdx.Config.ResolvedRules.IndexingDisabled {
			imageTree = &movedImageTree{
				targetSource: dstIdx.Name, targetDir: dstIdx.MakeIndexPath(realdst, true).String(),
				targetPath: realdst, version: directoryVersion,
			}
		}
		impacts = append(impacts, DirectoryRefresh{
			Source:  srcIdx.Name,
			Dir:     srcIdx.MakeIndexPath(filepath.Dir(realsrc), true).String(),
			Refresh: true,
			Deletes: []PendingDelete{{
				Path:      srcIdx.MakeIndexPath(realsrc, isSrcDir).String(),
				IsDir:     isSrcDir,
				Recursive: isSrcDir,
				imageTree: imageTree,
			}},
		})
	}
	if !dstIdx.Config.ResolvedRules.IndexingDisabled {
		impact := DirectoryRefresh{
			Source:  dstIdx.Name,
			Dir:     dstIdx.MakeIndexPath(filepath.Dir(realdst), true).String(),
			Refresh: true,
		}
		if isSrcDir {
			impact.Deep = []string{dstIdx.MakeIndexPath(realdst, true).String()}
		}
		impacts = append(impacts, impact)
	}
	return impacts
}
