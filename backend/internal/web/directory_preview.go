package web

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/files"
	"github.com/gtsteffaniak/filebrowser/backend/internal/database/users"
	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/gtsteffaniak/filebrowser/backend/internal/state"
	"github.com/gtsteffaniak/filebrowser/backend/internal/utils"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/gtsteffaniak/go-logger/logger"
)

const (
	directoryPreviewFrames          = 4
	directoryPreviewQueryPageSize   = 16
	directoryPreviewMaxIndexInspect = 256
)

type directoryPreviewLocation struct {
	source         string
	requestPath    string
	indexPath      string
	user           *users.User
	showHidden     bool
	hideFileExt    string
	followSymlinks bool
}

func resolveDirectoryPreviewLocation(d *Context) (directoryPreviewLocation, error) {
	location := directoryPreviewLocation{
		source: d.FileInfo.Source, requestPath: d.FileInfo.Path, indexPath: d.FileInfo.Path, user: d.User,
		showHidden: d.User != nil && d.User.ShowHidden,
	}
	if d.Share.Hash == "" {
		if d.User != nil {
			userScope, err := d.User.GetScopeForSourceName(location.source)
			if err != nil {
				return directoryPreviewLocation{}, err
			}
			location.indexPath = utils.AddTrailingSlashIfNotExists(utils.JoinScopedIndexPath(userScope, location.requestPath))
			location.hideFileExt = d.User.HideFileExt
		}
		return location, nil
	}
	sourceInfo, ok := settings.Config.Server.SourceMap[d.Share.SourcePath]
	if !ok {
		return directoryPreviewLocation{}, fmt.Errorf("source not found for share")
	}
	location.source = sourceInfo.Name
	location.requestPath = d.IndexPath
	location.indexPath = d.IndexPath
	location.user = d.ShareUser
	location.showHidden = d.Share.ShowHidden
	location.hideFileExt = d.Share.HideFileExt
	location.followSymlinks = true
	return location, nil
}

// indexedDirectoryPreviewPaths returns at most four accessible candidates and a
// boolean indicating whether the index was fresh enough to be authoritative.
func indexedDirectoryPreviewPaths(ctx context.Context, d *Context, location directoryPreviewLocation) ([]string, bool, error) {
	idx := indexing.GetIndex(location.source)
	if idx == nil || idx.Config.ResolvedRules.IndexingDisabled || !idx.Config.ResolvedRules.NoRules || idx.GetStatus() != indexing.READY {
		return nil, false, nil
	}
	indexedDir, ok := idx.GetReducedMetadata(location.indexPath, true)
	if !ok || indexedDir.ModTime.Unix() != d.FileInfo.ModTime.Unix() {
		return nil, false, nil
	}

	paths := make([]string, 0, directoryPreviewFrames)
	offset := 0
	for len(paths) < directoryPreviewFrames && offset < directoryPreviewMaxIndexInspect {
		if err := ctx.Err(); err != nil {
			return nil, true, err
		}
		page, err := idx.GetDirectoryPreviewCandidates(location.indexPath, location.showHidden, directoryPreviewQueryPageSize, offset)
		if err != nil {
			return nil, false, err
		}
		for _, candidate := range page {
			if location.hideFileExt != "" && utils.HideFileByExt(candidate.Name, location.hideFileExt) {
				continue
			}
			if !state.AccessPermitted(idx.Path, utils.IndexPathFromNormalized(candidate.Path, true), accessCheckUsername(d)) {
				continue
			}
			paths = append(paths, candidate.Name)
			if len(paths) == directoryPreviewFrames {
				break
			}
		}
		offset += len(page)
		if len(page) < directoryPreviewQueryPageSize {
			return paths, true, nil
		}
	}
	if len(paths) < directoryPreviewFrames {
		// Access rules hid a very large prefix. Fall back to the live, permission-
		// filtered listing rather than changing cover selection semantics.
		return nil, false, nil
	}
	return paths, true, nil
}

func liveDirectoryPreviewPaths(location directoryPreviewLocation) ([]string, error) {
	listing, err := files.FileInfoFaster(utils.FileOptions{
		Path: location.requestPath, Source: location.source, Expand: true,
		ShowHidden: location.showHidden, HideFileExt: location.hideFileExt,
		FollowSymlinks: location.followSymlinks,
	}, location.user)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, directoryPreviewFrames)
	for _, item := range listing.Files {
		if !iteminfo.ShouldBubbleUpToFolderPreview(item.ItemInfo) {
			continue
		}
		paths = append(paths, item.Name)
		if len(paths) == directoryPreviewFrames {
			break
		}
	}
	return paths, nil
}

func previewFileFromPaths(ctx context.Context, d *Context, location directoryPreviewLocation, paths []string, frameIndex int) (*iteminfo.ExtendedFileInfo, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("no previewable files found in directory")
	}
	index := frameIndex % len(paths)
	path := utils.JoinPathAsUnix(location.requestPath, paths[index])
	fileInfo, err := files.FileInfoFaster(utils.FileOptions{
		Path: path, Source: location.source, AlbumArt: true, Metadata: true,
	}, location.user)
	if err != nil {
		return nil, err
	}
	tempCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	_, previewErr := preview.GetPreviewForFile(tempCtx, *fileInfo, "small", "", 0)
	cancel()
	if previewErr == nil {
		return fileInfo, nil
	}
	if !errors.Is(previewErr, context.Canceled) && !errors.Is(previewErr, context.DeadlineExceeded) {
		logger.Debugf("Skipping preview file in directory '%s': %s (error: %v)", d.FileInfo.Name, path, previewErr)
		if index != 0 {
			return previewFileFromPaths(ctx, d, location, paths, 0)
		}
	}
	return nil, previewErr
}

// getDirectoryPreview returns the previewable file at the given frame index (0–3).
// The normal path reads a four-item indexed window; a live full listing is retained
// only for a disabled, incomplete, stale, or unusable index.
func getDirectoryPreview(ctx context.Context, d *Context, frameIndex int) (*iteminfo.ExtendedFileInfo, error) {
	location, err := resolveDirectoryPreviewLocation(d)
	if err != nil {
		return nil, err
	}
	paths, authoritative, indexErr := indexedDirectoryPreviewPaths(ctx, d, location)
	if indexErr != nil {
		if isClientCancellation(ctx, indexErr) || errors.Is(indexErr, context.DeadlineExceeded) {
			return nil, indexErr
		}
		logger.Debugf("Indexed directory preview fallback for '%s': %v", location.indexPath, indexErr)
	}
	if authoritative {
		fileInfo, previewErr := previewFileFromPaths(ctx, d, location, paths, frameIndex)
		if previewErr == nil || isClientCancellation(ctx, previewErr) {
			return fileInfo, previewErr
		}
		logger.Debugf("Indexed directory preview candidate failed for '%s', using live fallback: %v", location.indexPath, previewErr)
	}
	paths, err = liveDirectoryPreviewPaths(location)
	if err != nil {
		return nil, err
	}
	return previewFileFromPaths(ctx, d, location, paths, frameIndex)
}
