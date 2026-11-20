package web

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/files"
	indexsql "github.com/gtsteffaniak/filebrowser/backend/internal/database/sql"
	"github.com/gtsteffaniak/filebrowser/backend/internal/database/sqlitebusy"
	"github.com/gtsteffaniak/filebrowser/backend/internal/database/users"
	libErrors "github.com/gtsteffaniak/filebrowser/backend/internal/errors"
	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/gtsteffaniak/filebrowser/backend/internal/state"
	"github.com/gtsteffaniak/filebrowser/backend/internal/utils"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
)

const (
	listingDefaultLimit    = 100
	listingMaxLimit        = 500
	listingRevisionVersion = "paged-listing-rev-v1"
	listingCursorVersion   = 1
	listingMaxCursorBytes  = 8192
	listingKindDir         = "dir"
	listingKindFile        = "file"
	// Bounds how many SQL pages may be scanned while access rules filter rows,
	// so an all-denied directory cannot turn one request into a full scan.
	listingMaxFilteredPages = 20
)

// listingProtocolError carries a machine-readable code through the existing
// HttpResponse envelope without changing the shape of any old error response.
type listingProtocolError struct {
	status  int
	code    string
	message string
}

func (e *listingProtocolError) Error() string { return e.message }

// ErrorCode is consumed by wrapHandler and emitted as HttpResponse.code.
func (e *listingProtocolError) ErrorCode() string { return e.code }

func newListingProtocolError(status int, code, format string, args ...interface{}) error {
	return &listingProtocolError{status: status, code: code, message: fmt.Sprintf(format, args...)}
}

type listingOptions struct {
	Sort     string
	Order    string
	Limit    int
	Cursor   string
	Revision string
}

type listingCursorToken struct {
	Version    int    `json:"v"`
	Source     string `json:"s"`
	Directory  string `json:"d"`
	Sort       string `json:"sort"`
	Order      string `json:"order"`
	Kind       string `json:"kind"`
	Key        string `json:"key"`
	Path       string `json:"path"`
	ShowHidden bool   `json:"hidden"`
	HideExt    string `json:"hideExt,omitempty"`
	Revision   string `json:"rev"`
}

type listingItem struct {
	iteminfo.ItemInfo
	Path string `json:"path"`
	// ViewToken mirrors the legacy directory listing so inline viewing and
	// shares keep working without a session cookie.
	ViewToken string `json:"viewToken,omitempty"`
}

type listingResponse struct {
	Path        string        `json:"path"`
	Source      string        `json:"source,omitempty"`
	Name        string        `json:"name,omitempty"`
	Type        string        `json:"type,omitempty"`
	Sort        string        `json:"sort"`
	Order       string        `json:"order"`
	Limit       int           `json:"limit"`
	Revision    string        `json:"revision"`
	Items       []listingItem `json:"items"`
	HasMore     bool          `json:"hasMore"`
	Filtered    int           `json:"filtered"`
	PinnedItems []string      `json:"pinnedItems,omitempty"`
	NextCursor  string        `json:"nextCursor,omitempty"`
}

// listingRequest is the resolved, permission-checked form of an authenticated
// or public-share directory listing request.
type listingRequest struct {
	source      string
	viewToken   string
	requestPath string
	user        *users.User
	username    string
	showHidden  bool
	hideFileExt string
	clientRoot  string // full index path prefix to strip from item paths; empty = user scope
	clientPath  string // client-visible directory path for the response envelope
}

func parseListingOptions(r *http.Request) (listingOptions, error) {
	q := r.URL.Query()
	opts := listingOptions{
		Sort:     strings.ToLower(strings.TrimSpace(q.Get("sort"))),
		Order:    strings.ToLower(strings.TrimSpace(q.Get("order"))),
		Cursor:   q.Get("cursor"),
		Revision: q.Get("revision"),
	}
	if opts.Sort == "" {
		opts.Sort = string(indexsql.ListingSortName)
	}
	switch indexsql.ListingSort(opts.Sort) {
	case indexsql.ListingSortName, indexsql.ListingSortSize, indexsql.ListingSortModTime, indexsql.ListingSortType:
	default:
		return listingOptions{}, newListingProtocolError(http.StatusBadRequest, "invalid_sort", "sort must be one of name, size, mod_time, type")
	}
	if opts.Order == "" {
		opts.Order = string(indexsql.ListingOrderAsc)
	}
	switch indexsql.ListingOrder(opts.Order) {
	case indexsql.ListingOrderAsc, indexsql.ListingOrderDesc:
	default:
		return listingOptions{}, newListingProtocolError(http.StatusBadRequest, "invalid_order", "order must be asc or desc")
	}
	opts.Limit = listingDefaultLimit
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > listingMaxLimit {
			return listingOptions{}, newListingProtocolError(http.StatusBadRequest, "invalid_limit", "limit must be between 1 and %d", listingMaxLimit)
		}
		opts.Limit = n
	}
	if len(opts.Cursor) > listingMaxCursorBytes {
		return listingOptions{}, newListingProtocolError(http.StatusBadRequest, "invalid_cursor", "cursor is too large")
	}
	return opts, nil
}

func parseListingAuthRequest(r *http.Request) (string, string, listingOptions, error) {
	opts, err := parseListingOptions(r)
	if err != nil {
		return "", "", listingOptions{}, err
	}
	q := r.URL.Query()
	source := strings.TrimSpace(q.Get("source"))
	if source == "" {
		return "", "", listingOptions{}, newListingProtocolError(http.StatusBadRequest, "source_required", "source query parameter is required")
	}
	if !q.Has("path") {
		return "", "", listingOptions{}, newListingProtocolError(http.StatusBadRequest, "path_required", "path query parameter is required")
	}
	cleanPath, err := utils.SanitizePath(q.Get("path"))
	if err != nil {
		return "", "", listingOptions{}, newListingProtocolError(http.StatusBadRequest, "invalid_path", "%v", err)
	}
	return source, cleanPath, opts, nil
}

func listingClientDirPath(requestPath string) string {
	clean := utils.JoinPathAsUnix("/", requestPath)
	if clean != "/" {
		clean = utils.AddTrailingSlashIfNotExists(clean)
	}
	return clean
}

// resourceListingHandler is the authenticated paged listing endpoint. The legacy
// expanded /api/resources response is untouched.
func resourceListingHandler(w http.ResponseWriter, r *http.Request, d *Context) (int, error) {
	source, requestPath, opts, err := parseListingAuthRequest(r)
	if err != nil {
		return listingStatus(err), err
	}
	filePerms, err := effectiveFilePerms(d, source)
	if err != nil {
		return http.StatusForbidden, err
	}
	if !filePerms.View {
		return http.StatusForbidden, fmt.Errorf("user is not allowed to view files in this source")
	}
	viewToken := ""
	if canMintViewToken(d, source) {
		if minted, mintErr := mintViewGrant(d, source); mintErr == nil {
			viewToken = minted
		}
	}
	return serveDirectoryListing(r.Context(), listingRequest{
		source:      source,
		viewToken:   viewToken,
		requestPath: requestPath,
		user:        d.User,
		username:    accessCheckUsername(d),
		showHidden:  d.User.ShowHidden,
		hideFileExt: d.User.HideFileExt,
		clientPath:  listingClientDirPath(requestPath),
	}, opts, w, r)
}

// publicListingHandler is the public-share equivalent. It reuses the share
// owner's index scope but emits share-relative item paths.
func publicListingHandler(w http.ResponseWriter, r *http.Request, d *Context) (int, error) {
	if d.Share.ShareType == "upload" {
		return http.StatusNotImplemented, fmt.Errorf("browsing is disabled for upload shares")
	}
	sourceInfo, ok := settings.Config.Server.SourceMap[d.Share.SourcePath]
	if !ok {
		return http.StatusNotFound, fmt.Errorf("source not found")
	}
	opts, err := parseListingOptions(r)
	if err != nil {
		return listingStatus(err), err
	}
	clientPath := "/"
	if raw := strings.TrimSpace(r.URL.Query().Get("path")); raw != "" {
		cleanPath, err := utils.SanitizePath(raw)
		if err != nil {
			return http.StatusBadRequest, newListingProtocolError(http.StatusBadRequest, "invalid_path", "%v", err)
		}
		clientPath = listingClientDirPath(cleanPath)
	}
	viewToken := ""
	if canMintViewToken(d, sourceInfo.Name) {
		if minted, mintErr := mintViewGrant(d, sourceInfo.Name); mintErr == nil {
			viewToken = minted
		}
	}
	return serveDirectoryListing(r.Context(), listingRequest{
		source:      sourceInfo.Name,
		viewToken:   viewToken,
		requestPath: d.IndexPath,
		user:        d.ShareUser,
		username:    accessCheckUsername(d),
		showHidden:  d.Share.ShowHidden,
		hideFileExt: d.Share.HideFileExt,
		clientRoot:  utils.AddTrailingSlashIfNotExists(d.Share.Path),
		clientPath:  clientPath,
	}, opts, w, r)
}

// listingPageSource fetches one SQL page; it is injected so the filtering loop
// can be tested without a database or the global access rules.
type listingPageSource func(after *indexsql.ListingCursorKey, limit int) (*indexsql.DirectoryListingResult, error)

// collectVisibleListingPage walks SQL pages until enough *visible* rows are
// collected. Access rules can hide arbitrary rows, so a short or empty page is
// not completion, and the cursor must be derived from a visible row only.
func collectVisibleListingPage(first *indexsql.DirectoryListingResult, limit int, fetch listingPageSource, permitted func(*iteminfo.FileInfo) bool) ([]*iteminfo.FileInfo, int, bool, error) {
	items := make([]*iteminfo.FileInfo, 0, limit)
	filtered := 0
	current := first
	for pages := 0; ; pages++ {
		for _, item := range current.Items {
			if !permitted(item) {
				filtered++
				continue
			}
			items = append(items, item)
		}
		if len(items) >= limit || !current.HasMore || current.Next == nil {
			break
		}
		if pages >= listingMaxFilteredPages {
			return nil, filtered, false, newListingProtocolError(http.StatusServiceUnavailable, "listing_scan_budget", "access rules filtered too many rows; refresh or narrow the request")
		}
		remaining := limit - len(items)
		if remaining <= 0 {
			break
		}
		next, err := fetch(current.Next, remaining)
		if err != nil {
			return nil, filtered, false, err
		}
		if next.Fingerprint != first.Fingerprint {
			return nil, filtered, false, newListingProtocolError(http.StatusConflict, "revision_mismatch", "directory changed while paging")
		}
		current = next
	}
	hasMore := current.HasMore && len(items) > 0
	return items, filtered, hasMore, nil
}

func serveDirectoryListing(ctx context.Context, req listingRequest, opts listingOptions, w http.ResponseWriter, r *http.Request) (int, error) {
	if req.user == nil {
		return http.StatusForbidden, fmt.Errorf("user context not set")
	}
	fileOpts := utils.FileOptions{
		Path:           req.requestPath,
		Source:         req.source,
		FollowSymlinks: true,
		ShowHidden:     req.showHidden,
		HideFileExt:    req.hideFileExt,
	}
	indexPath, userScope, err := files.CheckPermissions(fileOpts, req.user)
	if err != nil {
		if errors.Is(err, libErrors.ErrAccessDenied) {
			return http.StatusForbidden, err
		}
		return ErrToStatus(err), err
	}
	defer preview.TrackWarmupBrowse()()
	indexPath = utils.AddTrailingSlashIfNotExists(indexPath)

	liveInfo, err := files.FileInfoFaster(utils.FileOptions{
		Path:              req.requestPath,
		Source:            req.source,
		FollowSymlinks:    true,
		ShowHidden:        req.showHidden,
		HideFileExt:       req.hideFileExt,
		SkipExtendedAttrs: true,
		ShowPinnedItems:   true,
	}, req.user)
	if err != nil {
		return ErrToStatus(err), err
	}
	if liveInfo.Type != "directory" {
		return http.StatusBadRequest, newListingProtocolError(http.StatusBadRequest, "not_a_directory", "path is not a directory")
	}

	preview.NoteWarmupDirectory(req.source, liveInfo.RealPath)
	db, err := ensureListingIndexReady(req.source, indexPath, liveInfo.ModTime)
	if err != nil {
		return listingStatus(err), err
	}

	var after *indexsql.ListingCursorKey
	var cursor listingCursorToken
	if opts.Cursor != "" {
		cursor, err = decodeListingCursor(opts.Cursor)
		if err != nil {
			return http.StatusBadRequest, newListingProtocolError(http.StatusBadRequest, "invalid_cursor", "%v", err)
		}
		if err := listingCursorMatches(cursor, req, opts, indexPath); err != nil {
			return listingStatus(err), err
		}
		if opts.Revision == "" {
			return http.StatusBadRequest, newListingProtocolError(http.StatusBadRequest, "revision_required", "revision is required when cursor is present")
		}
		after = &indexsql.ListingCursorKey{Kind: cursor.Kind, Key: cursor.Key, Path: cursor.Path}
	}

	result, err := db.GetDirectoryListingPage(ctx, req.source, indexPath, indexsql.DirectoryListingQuery{
		Sort:        indexsql.ListingSort(opts.Sort),
		Order:       indexsql.ListingOrder(opts.Order),
		Limit:       opts.Limit,
		After:       after,
		ShowHidden:  req.showHidden,
		HideFileExt: req.hideFileExt,
	})
	if err != nil {
		if errors.Is(err, sqlitebusy.ErrBusy) {
			return http.StatusServiceUnavailable, newListingProtocolError(http.StatusServiceUnavailable, "listing_busy", "index is busy; retry the request")
		}
		return http.StatusInternalServerError, err
	}
	if result.Fingerprint.SortKeysMissing {
		return http.StatusServiceUnavailable, newListingProtocolError(http.StatusServiceUnavailable, "index_rebuild_required", "index sort keys are incomplete; a full rescan is required")
	}

	revision := listingRevision(req.source, indexPath, liveInfo.ModTime, result.Fingerprint)
	if err := listingRevisionMismatch(opts.Revision, revision); err != nil {
		return listingStatus(err), err
	}
	if after != nil {
		if err := listingRevisionMismatch(cursor.Revision, revision); err != nil {
			return listingStatus(err), err
		}
	}

	prefix := req.clientRoot
	if prefix == "" {
		prefix = userScope
	}
	username := req.username
	sourcePath := indexPathSourcePath(req.source)
	visible, filtered, hasMore, err := collectVisibleListingPage(result, opts.Limit, func(after *indexsql.ListingCursorKey, limit int) (*indexsql.DirectoryListingResult, error) {
		next, qerr := db.GetDirectoryListingPage(ctx, req.source, indexPath, indexsql.DirectoryListingQuery{
			Sort:        indexsql.ListingSort(opts.Sort),
			Order:       indexsql.ListingOrder(opts.Order),
			Limit:       limit,
			After:       after,
			ShowHidden:  req.showHidden,
			HideFileExt: req.hideFileExt,
		})
		if qerr != nil {
			if errors.Is(qerr, sqlitebusy.ErrBusy) {
				return nil, newListingProtocolError(http.StatusServiceUnavailable, "listing_busy", "index is busy; retry the request")
			}
			return nil, qerr
		}
		return next, nil
	}, func(item *iteminfo.FileInfo) bool {
		return state.AccessPermitted(sourcePath, utils.IndexPathFromNormalized(item.Path, item.IsDir), username)
	})
	if err != nil {
		return listingStatus(err), err
	}
	items := make([]listingItem, 0, len(visible))
	var lastVisible *iteminfo.FileInfo
	for _, item := range visible {
		entry := listingItem{ItemInfo: item.ItemInfo, Path: listingClientPath(item.Path, prefix)}
		if !item.IsDir && req.viewToken != "" {
			entry.ViewToken = req.viewToken
		}
		items = append(items, entry)
		lastVisible = item
	}
	nextCursor := ""
	if hasMore {
		kind := listingKindFile
		if lastVisible.IsDir {
			kind = listingKindDir
		}
		cursor := listingCursorToken{
			Version:    listingCursorVersion,
			Source:     req.source,
			Directory:  indexPath,
			Sort:       opts.Sort,
			Order:      opts.Order,
			Kind:       kind,
			Key:        indexsql.ListingKeyForItem(indexsql.ListingSort(opts.Sort), lastVisible),
			Path:       lastVisible.Path,
			ShowHidden: req.showHidden,
			HideExt:    req.hideFileExt,
			Revision:   revision,
		}
		nextCursor, err = encodeListingCursor(cursor)
		if err != nil {
			return http.StatusInternalServerError, err
		}
	}

	resp := &listingResponse{
		Path:        req.clientPath,
		Source:      req.source,
		Name:        liveInfo.Name,
		Type:        liveInfo.Type,
		Sort:        opts.Sort,
		Order:       opts.Order,
		Limit:       opts.Limit,
		Revision:    revision,
		Items:       items,
		HasMore:     hasMore,
		Filtered:    filtered,
		PinnedItems: liveInfo.PinnedItems,
		NextCursor:  nextCursor,
	}
	return RenderJSON(w, r, resp)
}

// indexPathSourcePath resolves the filesystem source root used by access rules.
func indexPathSourcePath(source string) string {
	idx := indexing.GetIndex(source)
	if idx == nil {
		return ""
	}
	return idx.Path
}

func ensureListingIndexReady(source, indexPath string, liveModTime time.Time) (*indexsql.IndexDB, error) {
	idx := indexing.GetIndex(source)
	if idx == nil {
		return nil, newListingProtocolError(http.StatusServiceUnavailable, "index_unavailable", "source index is not available")
	}
	if idx.Config.ResolvedRules.IndexingDisabled {
		return nil, newListingProtocolError(http.StatusServiceUnavailable, "index_unavailable", "indexing is disabled for this source")
	}
	if !idx.Config.ResolvedRules.NoRules {
		return nil, newListingProtocolError(http.StatusServiceUnavailable, "index_incomplete", "conditional index rules can make the index an incomplete listing")
	}
	if idx.GetStatus() != indexing.READY {
		return nil, newListingProtocolError(http.StatusServiceUnavailable, "index_not_ready", "source index is not ready")
	}
	db := indexing.GetIndexDB()
	if db == nil {
		return nil, newListingProtocolError(http.StatusServiceUnavailable, "index_unavailable", "index database is not available")
	}
	dirInfo, ok := idx.GetReducedMetadata(indexPath, true)
	if !ok {
		return nil, newListingProtocolError(http.StatusServiceUnavailable, "index_stale", "directory is not indexed yet")
	}
	if dirInfo.ModTime.Unix() != liveModTime.Unix() {
		return nil, newListingProtocolError(http.StatusServiceUnavailable, "index_stale", "index is older than the live directory; retry after indexing")
	}
	return db, nil
}

func listingRevision(source, indexPath string, liveModTime time.Time, fp indexsql.DirectoryListingFingerprint) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%d\x00%d\x00%d\x00%t",
		listingRevisionVersion, source, indexPath, liveModTime.Unix(),
		fp.Count, fp.DirectoryUpdated, fp.SortKeysMissing)
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:16])
}

func listingCursorMatches(cursor listingCursorToken, req listingRequest, opts listingOptions, indexPath string) error {
	if cursor.Source != req.source || cursor.Directory != indexPath ||
		cursor.Sort != opts.Sort || cursor.Order != opts.Order ||
		cursor.ShowHidden != req.showHidden || cursor.HideExt != req.hideFileExt {
		return newListingProtocolError(http.StatusBadRequest, "cursor_mismatch", "cursor does not belong to this directory, sort, order, or filter set")
	}
	return nil
}

func listingRevisionMismatch(requested, current string) error {
	if requested != "" && requested != current {
		return newListingProtocolError(http.StatusConflict, "revision_mismatch", "directory changed while paginating; restart from the first page")
	}
	return nil
}

func encodeListingCursor(cursor listingCursorToken) (string, error) {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeListingCursor(raw string) (listingCursorToken, error) {
	var cursor listingCursorToken
	if raw == "" {
		return cursor, fmt.Errorf("empty cursor")
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return cursor, fmt.Errorf("cursor is not valid base64url")
	}
	if len(payload) > listingMaxCursorBytes {
		return cursor, fmt.Errorf("cursor is too large")
	}
	if err = json.Unmarshal(payload, &cursor); err != nil {
		return cursor, fmt.Errorf("cursor payload is invalid")
	}
	if cursor.Version != listingCursorVersion {
		return cursor, fmt.Errorf("unsupported cursor version")
	}
	if cursor.Kind != listingKindDir && cursor.Kind != listingKindFile {
		return cursor, fmt.Errorf("cursor kind is invalid")
	}
	if cursor.Path == "" || cursor.Key == "" || cursor.Source == "" || cursor.Directory == "" ||
		cursor.Sort == "" || cursor.Order == "" || cursor.Revision == "" {
		return cursor, fmt.Errorf("cursor is missing required fields")
	}
	return cursor, nil
}

func listingClientPath(fullIndexPath, prefix string) string {
	if prefix == "" || prefix == "/" {
		prefix = "/"
	} else {
		prefix = utils.AddTrailingSlashIfNotExists(prefix)
	}
	rel := strings.TrimPrefix(fullIndexPath, prefix)
	if rel == fullIndexPath {
		// Defensive: never leak a path that was not under the expected client root.
		rel = strings.TrimPrefix(fullIndexPath, "/")
	}
	if rel == "" {
		return "/"
	}
	if !strings.HasPrefix(rel, "/") {
		rel = "/" + rel
	}
	return rel
}

func listingStatus(err error) int {
	var protocolErr *listingProtocolError
	if errors.As(err, &protocolErr) {
		return protocolErr.status
	}
	return ErrToStatus(err)
}
