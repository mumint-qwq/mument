package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gtsteffaniak/filebrowser/backend/internal/database/users"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
)

func TestDiagnosticsRejectsAnonymousAndNoAuth(t *testing.T) {
	testDiagnosticsService(t)
	body := validDiagnosticsBatch()

	anonymous := httptest.NewRequest(http.MethodPost, "/diagnostics/client", bytes.NewReader(body))
	anonymous.Header.Set("Content-Type", "application/json")
	anonymous.Header.Set("X-Quantum-Diagnostics", "1")
	recorder := httptest.NewRecorder()
	status, _ := diagnosticsClientHandler(recorder, anonymous, &Context{User: &users.User{ID: 0, FrontendUser: users.FrontendUser{Username: "anonymous"}}})
	if status != http.StatusForbidden {
		t.Errorf("anonymous status=%d want 403", status)
	}
	status, _ = diagnosticsStatusHandler(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/diagnostics/status", nil), &Context{User: &users.User{ID: 0, FrontendUser: users.FrontendUser{Username: "anonymous"}}})
	if status != http.StatusForbidden {
		t.Errorf("anonymous status endpoint=%d want 403", status)
	}

	previous := settings.Config.Auth.Methods.NoAuth
	settings.Config.Auth.Methods.NoAuth = true
	defer func() { settings.Config.Auth.Methods.NoAuth = previous }()
	request := httptest.NewRequest(http.MethodPost, "/diagnostics/client", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Quantum-Diagnostics", "1")
	recorder = httptest.NewRecorder()
	status, _ = diagnosticsClientHandler(recorder, request, &Context{User: testUser()})
	if status != http.StatusForbidden {
		t.Errorf("NoAuth status=%d want 403", status)
	}
	status, _ = diagnosticsStatusHandler(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/diagnostics/status", nil), &Context{User: testUser()})
	if status != http.StatusForbidden {
		t.Errorf("NoAuth status endpoint=%d want 403", status)
	}
}

func TestDiagnosticsNotRegisteredOnPublicAPI(t *testing.T) {
	previousBase := settings.Config.Http.BaseURL
	settings.Config.Http.BaseURL = "/"
	defer func() { settings.Config.Http.BaseURL = previousBase }()
	router := http.NewServeMux()
	api := http.NewServeMux()
	publicRoutes := http.NewServeMux()
	publicApi := http.NewServeMux()
	configureHTTPRouter(router, api, publicRoutes, publicApi)
	request := httptest.NewRequest(http.MethodPost, "/diagnostics/client", bytes.NewReader(validDiagnosticsBatch()))
	recorder := httptest.NewRecorder()
	publicApi.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Errorf("public diagnostics route status=%d want 404", recorder.Code)
	}
}
