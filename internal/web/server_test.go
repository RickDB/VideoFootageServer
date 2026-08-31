package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RickDB/HikFootageServer/internal/auth"
	"github.com/RickDB/HikFootageServer/internal/config"
	"github.com/RickDB/HikFootageServer/internal/jobs"
	"github.com/RickDB/HikFootageServer/internal/store"
)

func testServer(t *testing.T) (*http.Server, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{ListenAddr: ":0", DatabasePath: filepath.Join(dir, "test.db"), ExportRoot: filepath.Join(dir, "exports"), MaxConcurrentExports: 1, SessionTTL: time.Hour, AdminUsername: "admin", AdminPassword: "change-me-now"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	authManager := auth.New(s, cfg)
	if err = authManager.BootstrapAdmin(t.Context(), cfg.AdminUsername, cfg.AdminPassword); err != nil {
		s.Close()
		t.Fatal(err)
	}
	return New(cfg, s, jobs.New(s, cfg, log), authManager, log), s
}

func TestUnauthenticatedRequestsAreBlocked(t *testing.T) {
	server, s := testServer(t)
	defer s.Close()
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusSeeOther || !strings.HasPrefix(response.Header().Get("Location"), "/login") {
		t.Fatalf("dashboard was not protected: %d %s", response.Code, response.Header().Get("Location"))
	}

	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/jobs/1", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("API status = %d, want 401", response.Code)
	}

	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Header().Get("X-HikFootage-Authentication") != "required" || !strings.Contains(response.Body.String(), "auth=required") {
		t.Fatalf("health endpoint does not identify authenticated build: headers=%v body=%q", response.Header(), response.Body.String())
	}
}

func TestLoginAndRequiredPasswordChange(t *testing.T) {
	server, s := testServer(t)
	defer s.Close()
	form := url.Values{"username": {"admin"}, "password": {"change-me-now"}, "next": {"/"}}
	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/account" {
		t.Fatalf("login result: %d %s", response.Code, response.Header().Get("Location"))
	}
	cookies := response.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("login did not set a session cookie")
	}

	request = httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(cookies[0])
	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || !strings.HasPrefix(response.Header().Get("Location"), "/account") {
		t.Fatalf("bootstrap password did not force account page: %d %s", response.Code, response.Header().Get("Location"))
	}

	form = url.Values{"current_password": {"change-me-now"}, "new_password": {"a-new-secure-password"}, "confirm_password": {"a-new-secure-password"}}
	request = httptest.NewRequest(http.MethodPost, "/account/password", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(cookies[0])
	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("password change status: %d body=%s", response.Code, response.Body.String())
	}
	newCookies := response.Result().Cookies()
	if len(newCookies) == 0 {
		t.Fatal("password change did not renew session")
	}

	request = httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(newCookies[0])
	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Secure historic footage") {
		t.Fatalf("authorized dashboard failed: %d", response.Code)
	}
}
