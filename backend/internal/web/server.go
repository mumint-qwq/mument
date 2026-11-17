// Modified from FileBrowser Quantum for the mument derivative. See MODIFICATIONS.md.
package web

import (
	"context"
	"crypto/tls"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	_ "net/http/pprof"

	"github.com/coreos/go-systemd/v22/activation"
	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/files"
	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/fileutils"
	"github.com/gtsteffaniak/filebrowser/backend/internal/auth"
	"github.com/gtsteffaniak/filebrowser/backend/internal/events"
	"github.com/gtsteffaniak/filebrowser/backend/internal/state"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/gtsteffaniak/go-logger/logger"
)

// Deps holds injected dependencies for HTTP handlers and middleware.
type Deps struct {
	Store *state.Store
	Files *files.Service
	Auth  *auth.Service
}

var (
	runtimeDeps Deps
	assetFs     fs.FS
)

func initRuntime(deps Deps, assets fs.FS) {
	runtimeDeps = deps
	assetFs = assets
}

// Embed the files in the frontend/dist directory
//
//go:embed embed/*
var assets embed.FS

// GetEmbeddedAssets returns the embedded assets filesystem
func GetEmbeddedAssets() embed.FS {
	return assets
}

// ShutdownResult reports whether the HTTP server actually drained its active
// handlers. Drained is true only when Shutdown returned nil and the request
// tracker observed no active handler afterwards.
type ShutdownResult struct {
	HTTPError      error
	ActiveHandlers int
	Drained        bool
}

type activeRequestTracker struct {
	active atomic.Int64
}

func (t *activeRequestTracker) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.active.Add(1)
		defer t.active.Add(-1)
		next.ServeHTTP(w, r)
	})
}

func (t *activeRequestTracker) count() int {
	return int(t.active.Load())
}

const httpShutdownGrace = 5 * time.Second

// shutdownHTTPServer stops accepting requests, waits for the server shutdown
// deadline, then gives active handlers one bounded grace period. It never
// treats a Shutdown error as "handlers are done".
func shutdownHTTPServer(srv *http.Server, tracker *activeRequestTracker, timeout, grace time.Duration) ShutdownResult {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	err := srv.Shutdown(shutdownCtx)
	cancel()
	if err != nil {
		logger.Errorf("HTTP server shutdown did not complete: %v", err)
	} else {
		logger.Info("HTTP server shut down gracefully.")
	}

	result := ShutdownResult{HTTPError: err, ActiveHandlers: tracker.count()}
	if result.ActiveHandlers > 0 {
		deadline := time.Now().Add(grace)
		for result.ActiveHandlers > 0 && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
			result.ActiveHandlers = tracker.count()
		}
	}
	result.Drained = err == nil && result.ActiveHandlers == 0
	return result
}

// StartHttp starts the HTTP server and blocks until ctx is cancelled.
func StartHttp(ctx context.Context, deps Deps, shutdownComplete chan ShutdownResult) {
	if settings.Env.IsDevMode {
		go func() {
			if err := http.ListenAndServe("localhost:6060", nil); err != nil {
				logger.Fatalf("pprof server error: %v", err)
			}
		}()
	}

	fs := fileutils.GetAssetFS()
	if fs == nil {
		logger.Fatal("Asset filesystem not initialized. Call fileutils.InitAssetFS first.")
	}

	templates := template.New("").Funcs(template.FuncMap{
		"marshal": func(v interface{}) (string, error) {
			a, err := json.Marshal(v)
			return string(a), err
		},
	})
	if !settings.Env.IsDevMode {
		templates = template.Must(templates.ParseFS(fs, "public/index.html"))
	}
	templateRenderer = &TemplateRenderer{
		templates: templates,
		devMode:   settings.Env.IsDevMode,
	}

	initRuntime(deps, fs)
	initDiagnosticsRuntime()

	router := http.NewServeMux()
	api := http.NewServeMux()
	publicRoutes := http.NewServeMux()
	publicApi := http.NewServeMux()

	configureHTTPRouter(router, api, publicRoutes, publicApi)

	tracker := &activeRequestTracker{}
	srv := &http.Server{
		Addr:    settings.HTTPListenAddr(settings.Config.Http.ListenAddress, settings.Config.Http.Port),
		Handler: tracker.wrap(muxWithMiddleware(router)),
	}
	listenAddress := settings.Config.Http.ListenAddress
	go func() {
		var listener net.Listener
		var err error

		if settings.Config.Http.Socket != "" {
			if err = os.Remove(settings.Config.Http.Socket); err != nil && !os.IsNotExist(err) {
				logger.Fatalf("Could not remove existing socket: %v", err)
			}
			listener, err = net.Listen("unix", settings.Config.Http.Socket)
			if err != nil {
				logger.Fatalf("Could not listen on unix socket: %v", err)
			}
			logger.Infof("Running at               : unix://%s%s", settings.Config.Http.Socket, settings.Config.Http.BaseURL)
		} else if settings.Config.Http.TLSCert != "" && settings.Config.Http.TLSKey != "" {
			cer, err := tls.LoadX509KeyPair(settings.Config.Http.TLSCert, settings.Config.Http.TLSKey)
			if err != nil {
				logger.Fatalf("Could not load certificate: %v", err)
			}
			tlsConfig := &tls.Config{
				MinVersion:   tls.VersionTLS12,
				Certificates: []tls.Certificate{cer},
			}
			scheme := "https"
			port := ""
			if settings.Config.Http.Port != 443 {
				port = fmt.Sprintf(":%d", settings.Config.Http.Port)
			}
			fullURL := fmt.Sprintf("%s://%s%s%s", scheme, listenAddress, port, settings.Config.Http.BaseURL)
			logger.Infof("Running at               : %s", fullURL)

			socketActivationListeners, err := activation.TLSListeners(tlsConfig)
			if err == nil && len(socketActivationListeners) > 0 {
				listener = socketActivationListeners[0]
				logger.Debug("Socket activation detected. Listening address is being controlled by systemd.")
			} else if err != nil {
				logger.Debugf("Socket activation failed: %v", err)
			}
			if listener == nil {
				listener, err = tls.Listen("tcp", srv.Addr, tlsConfig)
				if err != nil {
					logger.Fatalf("Could not start TLS server: %v", err)
				}
			}
		} else {
			scheme := "http"
			port := ""
			if settings.Config.Http.Port != 80 {
				port = fmt.Sprintf(":%d", settings.Config.Http.Port)
			}
			fullURL := fmt.Sprintf("%s://%s%s%s", scheme, listenAddress, port, settings.Config.Http.BaseURL)
			logger.Infof("Running at               : %s", fullURL)

			socketActivationListeners, err := activation.Listeners()
			if err == nil && len(socketActivationListeners) > 0 {
				listener = socketActivationListeners[0]
				logger.Debug("Socket activation detected. Listening address is being controlled by systemd.")
			} else if err != nil {
				logger.Debugf("Socket activation failed: %v", err)
			}
			if listener == nil {
				addr := srv.Addr
				if addr == "" {
					addr = ":http"
				}
				listener, err = net.Listen("tcp", addr)
				if err != nil {
					logger.Fatalf("Server error: %v", err)
				}
			}
		}

		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("Server error: %v", err)
		}
	}()

	<-ctx.Done()
	logger.Info("Shutting down HTTP server...")
	events.Shutdown()

	result := shutdownHTTPServer(srv, tracker, 30*time.Second, httpShutdownGrace)
	if !result.Drained {
		logger.Errorf("HTTP shutdown incomplete: %d handler(s) still active (error: %v); leaving state, index database and caches open",
			result.ActiveHandlers, result.HTTPError)
	} else {
		if err := state.Close(); err != nil {
			logger.Errorf("Failed to close state management: %v", err)
		}
		if diagnosticsRuntime != nil {
			diagnosticsRuntime.Close(2 * time.Second)
		}
	}

	if settings.Config.Http.Socket != "" {
		if err := os.Remove(settings.Config.Http.Socket); err != nil && !os.IsNotExist(err) {
			logger.Debugf("Could not remove unix socket: %v", err)
		}
	}

	shutdownComplete <- result
	close(shutdownComplete)
}
