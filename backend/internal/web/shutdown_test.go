package web

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

// A Shutdown timeout must not be reported as "handlers drained": the tracker
// still sees the active handler, so the caller knows dependencies cannot be
// closed yet.
func TestShutdownHTTPServerReportsActiveHandlers(t *testing.T) {
	tracker := &activeRequestTracker{}
	started := make(chan struct{})
	release := make(chan struct{})
	srv := &http.Server{
		Handler: tracker.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(started)
			<-release
			w.WriteHeader(http.StatusOK)
		})),
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() { _ = srv.Serve(listener) }()

	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		resp, err := http.Get("http://" + listener.Addr().String())
		if err == nil {
			resp.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not start")
	}

	result := shutdownHTTPServer(srv, tracker, 100*time.Millisecond, 100*time.Millisecond)
	if result.Drained {
		t.Fatal("shutdown reported drained while a handler was still active")
	}
	if result.ActiveHandlers < 1 {
		t.Fatalf("ActiveHandlers = %d, want at least 1", result.ActiveHandlers)
	}
	if !errors.Is(result.HTTPError, context.DeadlineExceeded) {
		t.Fatalf("HTTPError = %v, want context deadline exceeded", result.HTTPError)
	}

	close(release)
	select {
	case <-clientDone:
	case <-time.After(5 * time.Second):
		t.Fatal("client did not finish")
	}

	// After the handler exits, a second shutdown has no active handlers and is
	// allowed to report a clean drain.
	result = shutdownHTTPServer(srv, tracker, 2*time.Second, 2*time.Second)
	if !result.Drained || result.ActiveHandlers != 0 {
		t.Fatalf("second shutdown = %+v, want drained with no active handlers", result)
	}
}
