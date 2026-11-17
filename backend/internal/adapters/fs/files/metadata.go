package files

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/database/users"
	apperrors "github.com/gtsteffaniak/filebrowser/backend/internal/errors"
	"github.com/gtsteffaniak/filebrowser/backend/internal/ffmpeg"
	"github.com/gtsteffaniak/filebrowser/backend/internal/utils"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/gtsteffaniak/go-cache/cache"
	"github.com/gtsteffaniak/go-logger/logger"
)

const (
	// MetadataSubsetMaxPaths caps a single subset request so one client cannot
	// queue an unbounded amount of probe work.
	MetadataSubsetMaxPaths = 100

	// MetadataSubsetRequestTimeout bounds the whole batch (including jobs that
	// are still queued when the request context is cancelled).
	MetadataSubsetRequestTimeout = 60 * time.Second
)

// Stable per-item messages returned to clients. Details stay in debug logs so
// filesystem paths and ffprobe stderr are not exposed.
const (
	metadataErrorUnavailable = "not found or access denied"
	metadataErrorUnsupported = "not an audio/video file"
	metadataErrorFailed      = "metadata extraction failed"
	metadataErrorTimedOut    = "metadata extraction timed out"
	metadataErrorCanceled    = "metadata request canceled"
)

// MetadataItem is one path's subset result. Error is populated per item so a
// failure never blocks the rest of the batch.
type MetadataItem struct {
	Path     string                  `json:"path"`
	Name     string                  `json:"name,omitempty"`
	Type     string                  `json:"type,omitempty"`
	Metadata *iteminfo.MediaMetadata `json:"metadata,omitempty"`
	Error    string                  `json:"error,omitempty"`
}

// MetadataResponse is the POST /api/resources/metadata payload.
type MetadataResponse struct {
	Items []MetadataItem `json:"items"`
}

// metadataExtractFunc is replaceable in tests to keep probes out of unit tests.
type metadataExtractFunc func(ctx context.Context, item *iteminfo.ExtendedItemInfo, realPath string, albumArt bool, svc *ffmpeg.FFmpegService) error

var extractMediaMetadataFunc metadataExtractFunc = extractMediaMetadataImpl

// mediaMetadataCache stores positive probe/tag results so repeated listing
// patches do not re-run ffprobe. Keys include the index modtime; entries expire
// on their own. Album-art responses are deliberately not cached (they can carry
// megabytes of image bytes).
var mediaMetadataCache = cache.NewCache[*iteminfo.MediaMetadata](2 * time.Hour)

type metadataTarget struct {
	resultIndex int
	slot        int
	path        string
	name        string
	typ         string
	realPath    string
	modTime     time.Time
}

type metadataResult struct {
	resultIndex int
	metadata    *iteminfo.MediaMetadata
	err         string
}

// NormalizeMetadataPaths trims, drops empty entries, removes duplicates while
// preserving order, and enforces the per-request cap.
func NormalizeMetadataPaths(paths []string, max int) ([]string, error) {
	if max <= 0 {
		max = MetadataSubsetMaxPaths
	}
	out := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, raw := range paths {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: paths is required", apperrors.ErrInvalidRequestParams)
	}
	if len(out) > max {
		return nil, fmt.Errorf("%w: at most %d paths per request", apperrors.ErrInvalidRequestParams, max)
	}
	return out, nil
}

// GetMetadataSubset resolves each requested path through the same permission
// checks as FileInfoFaster, then extracts audio/video metadata with a bounded
// worker pool. Invalid or inaccessible paths become per-item errors.
func GetMetadataSubset(ctx context.Context, opts utils.FileOptions, user *users.User, paths []string, albumArt bool) (MetadataResponse, error) {
	response := MetadataResponse{Items: []MetadataItem{}}
	if user == nil {
		return response, fmt.Errorf("%w: user not provided", apperrors.ErrInvalidRequestParams)
	}
	normalized, err := NormalizeMetadataPaths(paths, MetadataSubsetMaxPaths)
	if err != nil {
		return response, err
	}
	targets, results := resolveMetadataTargets(opts, user, normalized)
	media := settings.Config.Integrations.Media
	extracted := runMetadataExtraction(
		ctx,
		targets,
		albumArt,
		media.ResolvedMetadataWorkers(),
		media.ResolvedMetadataTimeout(),
		extractMediaMetadataFunc,
		ffmpeg.Get(),
	)
	for _, res := range extracted {
		if res.resultIndex < 0 || res.resultIndex >= len(results) {
			continue
		}
		results[res.resultIndex].Metadata = res.metadata
		results[res.resultIndex].Error = res.err
	}
	response.Items = results
	return response, nil
}

func resolveMetadataTargets(opts utils.FileOptions, user *users.User, paths []string) ([]metadataTarget, []MetadataItem) {
	results := make([]MetadataItem, len(paths))
	targets := make([]metadataTarget, 0, len(paths))
	idx := indexing.GetIndex(opts.Source)
	service := svc()
	for i, rawPath := range paths {
		results[i] = MetadataItem{Path: rawPath}
		if idx == nil {
			results[i].Error = metadataErrorUnavailable
			continue
		}
		clean, err := utils.SanitizePath(rawPath)
		if err != nil {
			results[i].Error = metadataErrorUnavailable
			continue
		}
		indexPath, userScope, err := checkPermissionsImpl(utils.FileOptions{
			Path:           clean,
			Source:         opts.Source,
			FollowSymlinks: opts.FollowSymlinks,
			ShowHidden:     opts.ShowHidden,
			HideFileExt:    opts.HideFileExt,
		}, user, service)
		if err != nil {
			results[i].Error = metadataErrorUnavailable
			continue
		}
		info, err := idx.GetFileInfo(indexing.FileInfoRequest{
			IndexPath:         indexPath,
			FollowSymlinks:    opts.FollowSymlinks,
			BoundIndexPath:    userScope,
			ShowHidden:        opts.ShowHidden,
			Expand:            false,
			SkipExtendedAttrs: true,
		})
		if err != nil {
			results[i].Error = metadataErrorUnavailable
			continue
		}
		results[i].Name = info.Name
		results[i].Type = info.Type
		if !isAudioVideoType(info.Type) {
			results[i].Error = metadataErrorUnsupported
			continue
		}
		realPath := utils.JoinUnderSourceRoot(idx.Path, indexPath)
		if opts.FollowSymlinks {
			if resolved, _, resolveErr := idx.GetRealPathScoped(userScope, indexPath); resolveErr == nil && resolved != "" {
				realPath = resolved
			}
		}
		targets = append(targets, metadataTarget{
			resultIndex: i,
			slot:        len(targets),
			path:        rawPath,
			name:        info.Name,
			typ:         info.Type,
			realPath:    realPath,
			modTime:     info.ModTime,
		})
	}
	return targets, results
}

func isAudioVideoType(itemType string) bool {
	return strings.HasPrefix(itemType, "audio") || strings.HasPrefix(itemType, "video")
}

// runMetadataExtraction processes targets with a fixed worker pool. It always
// returns one result per target (unscheduled targets become cancellation
// errors), which keeps callers free of partial-slice bookkeeping.
func runMetadataExtraction(ctx context.Context, targets []metadataTarget, albumArt bool, workers int, itemTimeout time.Duration, extract metadataExtractFunc, svc *ffmpeg.FFmpegService) []metadataResult {
	results := make([]metadataResult, len(targets))
	for i := range targets {
		results[i] = metadataResult{resultIndex: targets[i].resultIndex}
	}
	if len(targets) == 0 {
		return results
	}
	if workers < 1 {
		workers = 1
	}
	if itemTimeout <= 0 {
		itemTimeout = settings.DefaultMetadataTimeout
	}
	jobs := make(chan metadataTarget)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for target := range jobs {
				results[target.slot] = extractMetadataTarget(ctx, target, albumArt, itemTimeout, extract, svc)
			}
		}()
	}
	scheduled := 0
scheduleLoop:
	for _, target := range targets {
		// Prefer stopping over dispatch once the request is canceled; otherwise a
		// select with both cases ready may still hand a target to an idle worker.
		if ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
			break scheduleLoop
		case jobs <- target:
			scheduled++
		}
	}
	close(jobs)
	wg.Wait()
	for slot := scheduled; slot < len(targets); slot++ {
		if results[slot].err == "" {
			results[slot].err = metadataErrorCanceled
		}
	}
	return results
}

func extractMetadataTarget(ctx context.Context, target metadataTarget, albumArt bool, itemTimeout time.Duration, extract metadataExtractFunc, svc *ffmpeg.FFmpegService) metadataResult {
	result := metadataResult{resultIndex: target.resultIndex}
	// Album-art responses are not cached, so never satisfy them from the small
	// metadata cache either.
	if !albumArt {
		if cached, ok := loadCachedMediaMetadata(target); ok {
			result.metadata = cached
			return result
		}
	}
	item := &iteminfo.ExtendedItemInfo{
		ItemInfo: iteminfo.ItemInfo{
			Name:    target.name,
			Type:    target.typ,
			ModTime: target.modTime,
		},
	}
	itemCtx, cancel := context.WithTimeout(ctx, itemTimeout)
	err := extract(itemCtx, item, target.realPath, albumArt, svc)
	timedOut := errors.Is(itemCtx.Err(), context.DeadlineExceeded)
	cancel()
	switch {
	case err == nil:
		result.metadata = item.Metadata
		storeCachedMediaMetadata(target, albumArt, item.Metadata)
	case timedOut:
		result.err = metadataErrorTimedOut
		logger.Debugf("metadata subset: timed out extracting %s: %v", target.name, err)
	case ctx.Err() != nil || errors.Is(err, context.Canceled):
		result.err = metadataErrorCanceled
	case errors.Is(err, context.DeadlineExceeded):
		result.err = metadataErrorTimedOut
	default:
		result.err = metadataErrorFailed
		logger.Debugf("metadata subset: failed extracting %s: %v", target.name, err)
	}
	return result
}

func extractMediaMetadataImpl(ctx context.Context, item *iteminfo.ExtendedItemInfo, realPath string, albumArt bool, svc *ffmpeg.FFmpegService) error {
	if strings.HasPrefix(item.Type, "audio") {
		return extractAudioMetadata(ctx, item, realPath, albumArt, true, false, svc)
	}
	return extractVideoMetadata(ctx, item, realPath, svc)
}

func mediaMetadataCacheKey(target metadataTarget) string {
	return target.realPath + "|" + strconv.FormatInt(target.modTime.UnixNano(), 10)
}

func loadCachedMediaMetadata(target metadataTarget) (*iteminfo.MediaMetadata, bool) {
	if target.realPath == "" {
		return nil, false
	}
	return mediaMetadataCache.Get(mediaMetadataCacheKey(target))
}

func storeCachedMediaMetadata(target metadataTarget, albumArt bool, metadata *iteminfo.MediaMetadata) {
	if albumArt || target.realPath == "" {
		return
	}
	mediaMetadataCache.Set(mediaMetadataCacheKey(target), metadata)
}
