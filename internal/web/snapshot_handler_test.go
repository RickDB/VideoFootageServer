package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/RickDB/HikFootageServer/internal/store"
)

// login signs in as the given username/password and returns the resulting
// session cookie, following the same form-post flow TestLoginAndRequiredPasswordChange
// uses for the bootstrap admin.
func login(t *testing.T, server *http.Server, username, password string) *http.Cookie {
	t.Helper()
	form := url.Values{"username": {username}, "password": {password}, "next": {"/"}}
	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	cookies := response.Result().Cookies()
	if response.Code != http.StatusSeeOther || len(cookies) == 0 {
		t.Fatalf("login as %q failed: %d body=%s", username, response.Code, response.Body.String())
	}
	return cookies[0]
}

func TestCameraSnapshotUnauthenticatedIsBlocked(t *testing.T) {
	server, s := testServer(t)
	defer s.Close()
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/cameras/1/snapshot", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated snapshot request status = %d, want 401", response.Code)
	}
}

func TestCameraSnapshotUnknownCameraIs404(t *testing.T) {
	server, s := testServer(t)
	defer s.Close()
	ctx := t.Context()
	hash, err := bcrypt.GenerateFromPassword([]byte("a-password-123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUserFull(ctx, store.User{Username: "requester", PasswordHash: string(hash), Role: "collins", CustomerID: 1, AllSites: true}); err != nil {
		t.Fatal(err)
	}
	cookie := login(t, server, "requester", "a-password-123")

	request := httptest.NewRequest(http.MethodGet, "/api/cameras/999999/snapshot", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown camera status = %d, want 404", response.Code)
	}
}

func TestCameraSnapshotDeniesUserOutsideTheirCustomer(t *testing.T) {
	server, s := testServer(t)
	defer s.Close()
	ctx := t.Context()

	// Customer id 2 ("Collins") is seeded by the schema migration itself,
	// disjoint from the operational default (customer id 1) the requester
	// below is scoped to.
	const collinsCustomerID = 2
	siteID, err := s.CreateSite(ctx, store.Site{Name: "Collins HQ", CustomerID: collinsCustomerID, Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	nvrID, err := s.SaveNVR(ctx, store.NVR{SiteID: siteID, Name: "NVR1", Host: "192.0.2.1", Port: 80, Scheme: "http"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceCameras(ctx, nvrID, []store.Camera{{ChannelID: "101", Name: "Front door", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	cams, err := s.ListCameras(ctx, nvrID)
	if err != nil || len(cams) != 1 {
		t.Fatalf("expected one camera, got %v err=%v", cams, err)
	}

	// A request-only user scoped to a *different* customer (id 1, the
	// operational default) than the camera above belongs to.
	hash, err := bcrypt.GenerateFromPassword([]byte("a-password-123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUserFull(ctx, store.User{Username: "requester", PasswordHash: string(hash), Role: "collins", CustomerID: 1, AllSites: true, RequestScope: "own"}); err != nil {
		t.Fatal(err)
	}
	cookie := login(t, server, "requester", "a-password-123")

	request := httptest.NewRequest(http.MethodGet, "/api/cameras/"+strconv.FormatInt(cams[0].ID, 10)+"/snapshot", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-customer snapshot access status = %d, want 403, body=%s", response.Code, response.Body.String())
	}
}
