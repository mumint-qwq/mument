package web

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/diagnostics"
	"github.com/gtsteffaniak/filebrowser/backend/internal/version"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/gtsteffaniak/go-logger/logger"
)

const diagnosticsTraceHeader = "X-Quantum-Trace"

// DiagnosticsSpan is request-scoped correlation state. It is deliberately not
// exported to clients and never contains paths or query values.
type DiagnosticsSpan struct {
	RequestRef  string
	API         string
	IsDirectory bool
	KnownItems  *int
	Paged       bool
	Started     time.Time
}

var diagnosticsRuntime *diagnostics.Service

func initDiagnosticsRuntime() {
	build := version.Version
	if version.CommitSHA != "" && version.CommitSHA != "untracked" {
		build += "@" + version.CommitSHA
	}
	if len(build) > 96 {
		build = build[:96]
	}
	service, err := diagnostics.NewFromEnv(nil, build, logger.Warningf)
	if err != nil {
		logger.Warningf("diagnostics disabled: %v", err)
	}
	diagnosticsRuntime = service
	if service != nil && service.Enabled() {
		logger.Infof("Client diagnostics enabled (schema v1, dir configured)")
	}
}

func currentDiagnosticsStatus() diagnostics.Status {
	if diagnosticsRuntime == nil {
		return diagnostics.DisabledStatus()
	}
	return diagnosticsRuntime.Status()
}

func diagnosticsStatusHandler(w http.ResponseWriter, r *http.Request, d *Context) (int, error) {
	w.Header().Set("Cache-Control", "no-store")
	// Capability and receiver must agree: NoAuth/anonymous deployments do not
	// get a 200 "enabled" status that the POST endpoint would then reject.
	if diagnosticsAuthUnsupported(d) {
		return diagnosticsError(w, r, &diagnostics.AcceptError{Status: http.StatusForbidden, Code: "unsupported_auth", Message: "diagnostics requires an authenticated account"})
	}
	return RenderJSON(w, r, currentDiagnosticsStatus())
}

func diagnosticsAuthUnsupported(d *Context) bool {
	return settings.Config.Auth.Methods.NoAuth || d == nil || d.User == nil || d.User.ID == 0 || strings.EqualFold(d.User.Username, "anonymous")
}

func diagnosticsClientHandler(w http.ResponseWriter, r *http.Request, d *Context) (int, error) {
	w.Header().Set("Cache-Control", "no-store")
	if diagnosticsRuntime == nil || !diagnosticsRuntime.Enabled() {
		return diagnosticsError(w, r, &diagnostics.AcceptError{Status: http.StatusNotFound, Code: "diagnostics_disabled", Message: "diagnostics receiver is not enabled"})
	}
	if diagnosticsAuthUnsupported(d) {
		return diagnosticsError(w, r, &diagnostics.AcceptError{Status: http.StatusForbidden, Code: "unsupported_auth", Message: "diagnostics requires an authenticated account"})
	}
	if r.Header.Get("X-Quantum-Diagnostics") != "1" {
		return diagnosticsError(w, r, &diagnostics.AcceptError{Status: http.StatusBadRequest, Code: "missing_header", Message: "X-Quantum-Diagnostics: 1 is required"})
	}
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		return diagnosticsError(w, r, &diagnostics.AcceptError{Status: http.StatusUnsupportedMediaType, Code: "invalid_content_type", Message: "Content-Type must be application/json"})
	}
	if site := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))); site == "cross-site" {
		return diagnosticsError(w, r, &diagnostics.AcceptError{Status: http.StatusForbidden, Code: "cross_site_rejected", Message: "cross-site diagnostics requests are rejected"})
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin != "" && !diagnosticsRuntime.Config().OriginAllowed(origin) {
		return diagnosticsError(w, r, &diagnostics.AcceptError{Status: http.StatusForbidden, Code: "origin_rejected", Message: "origin is not allowed"})
	}

	limited := http.MaxBytesReader(w, r.Body, diagnostics.MaxBodyBytes)
	body, err := io.ReadAll(limited)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return diagnosticsError(w, r, &diagnostics.AcceptError{Status: http.StatusRequestEntityTooLarge, Code: "body_too_large", Message: "request body too large"})
		}
		return diagnosticsError(w, r, &diagnostics.AcceptError{Status: http.StatusBadRequest, Code: "read_failed", Message: "request body could not be read"})
	}
	result, acceptErr := diagnosticsRuntime.Accept(d.User.ID, body)
	if acceptErr != nil {
		return diagnosticsError(w, r, acceptErr)
	}
	return RenderJSON(w, r, result, http.StatusAccepted)
}

func diagnosticsError(w http.ResponseWriter, r *http.Request, acceptErr *diagnostics.AcceptError) (int, error) {
	if acceptErr == nil {
		return http.StatusInternalServerError, nil
	}
	if acceptErr.Status == http.StatusTooManyRequests && acceptErr.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(acceptErr.RetryAfter))
	}
	status := acceptErr.Status
	if status == 0 {
		status = http.StatusBadRequest
	}
	return RenderJSON(w, r, map[string]string{
		"code":    acceptErr.Code,
		"message": acceptErr.Message,
	}, status)
}

// withDiagnosticsSpan wraps authenticated directory handlers after withUser.
// Missing/invalid trace headers take the cheap branch and change nothing.
func withDiagnosticsSpan(api string, fn handleFunc) handleFunc {
	return func(w http.ResponseWriter, r *http.Request, d *Context) (int, error) {
		ref := diagnostics.ValidateRequestRef(r.Header.Get(diagnosticsTraceHeader))
		if ref == "" || diagnosticsRuntime == nil || !diagnosticsRuntime.Enabled() || d == nil || d.User == nil {
			return fn(w, r, d)
		}
		span := &DiagnosticsSpan{
			RequestRef:  ref,
			API:         api,
			IsDirectory: api == "resources_list",
			Paged:       api == "resources_list",
			Started:     time.Now(),
		}
		d.Diagnostics = span
		status, err := fn(w, r, d)
		if span.IsDirectory {
			outcome := "ok"
			if err != nil || status >= 400 {
				outcome = "error"
			}
			diagnosticsRuntime.RecordListingSpan(d.User.ID, ref, api, status, time.Since(span.Started), span.KnownItems, span.Paged, outcome)
		}
		return status, err
	}
}

// markDirectorySpanCount is called from the legacy directory handler only when
// the result is a directory. The count is read from already-built fields.
func markDirectorySpanCount(d *Context, count int) {
	if d == nil || d.Diagnostics == nil {
		return
	}
	d.Diagnostics.IsDirectory = true
	if count >= 0 && count <= 1000000 {
		d.Diagnostics.KnownItems = &count
	}
}
