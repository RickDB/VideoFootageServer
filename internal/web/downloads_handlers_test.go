package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/RickDB/HikFootageServer/internal/store"
)

func createTestUser(t *testing.T, s *store.Store, username, role string) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("a-password-123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUserFull(context.Background(), store.User{Username: username, PasswordHash: string(hash), Role: role, CustomerID: 1, AllSites: true}); err != nil {
		t.Fatal(err)
	}
}

// TestDownloaderReachesDownloadsButNotRequestReview pins down the new
// role's whole point: a "downloader" account can reach the Downloads area
// (everyone can) but is turned back from the request-review surfaces that
// reviewOnly gates, exactly like a "collins" requester would be.
func TestDownloaderReachesDownloadsButNotRequestReview(t *testing.T) {
	server, s := testServer(t)
	defer s.Close()
	createTestUser(t, s, "dl", "downloader")
	cookie := login(t, server, "dl", "a-password-123")

	get := func(path string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		server.Handler.ServeHTTP(response, request)
		return response
	}

	if resp := get("/downloads"); resp.Code != http.StatusOK {
		t.Fatalf("downloader GET /downloads = %d, want 200", resp.Code)
	}
	if resp := get("/exports/new"); resp.Code != http.StatusSeeOther || resp.Header().Get("Location") != "/requests" {
		t.Fatalf("downloader GET /exports/new = %d location=%q, want redirect to /requests", resp.Code, resp.Header().Get("Location"))
	}
	if resp := get("/users"); resp.Code != http.StatusSeeOther {
		t.Fatalf("downloader GET /users = %d, want a redirect away (opOnly)", resp.Code)
	}
}

// TestReviewerReachesExportWizardButNotUserManagement pins down reviewOnly
// vs opOnly: a reviewer gets everything needed to process footage requests
// (the export wizard) but never site/recorder/user/settings management,
// which stays strictly opOnly.
func TestReviewerReachesExportWizardButNotUserManagement(t *testing.T) {
	server, s := testServer(t)
	defer s.Close()
	createTestUser(t, s, "rev", "reviewer")
	cookie := login(t, server, "rev", "a-password-123")

	get := func(path string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		server.Handler.ServeHTTP(response, request)
		return response
	}

	if resp := get("/exports/new"); resp.Code != http.StatusOK {
		t.Fatalf("reviewer GET /exports/new = %d, want 200", resp.Code)
	}
	if resp := get("/downloads"); resp.Code != http.StatusOK {
		t.Fatalf("reviewer GET /downloads = %d, want 200", resp.Code)
	}
	if resp := get("/users"); resp.Code != http.StatusSeeOther {
		t.Fatalf("reviewer GET /users = %d, want a redirect away (opOnly)", resp.Code)
	}
}

// TestCollinsDownloadIsScopedToOwnRequest is the end-to-end version of the
// store-level JobLinkedToRequester check: a request-only user can download
// the file from a job linked to their own request, but a 403 (not a leak)
// for one linked to someone else's.
func TestCollinsDownloadIsScopedToOwnRequest(t *testing.T) {
	server, s := testServer(t)
	defer s.Close()
	ctx := context.Background()

	siteID, err := s.CreateSite(ctx, store.Site{Customer: "Acme", Name: "HQ", Timezone: "UTC", CustomerID: 1})
	if err != nil {
		t.Fatal(err)
	}
	nvrID, err := s.SaveNVR(ctx, store.NVR{SiteID: siteID, Name: "NVR1", Host: "192.0.2.5", Port: 80, Scheme: "http"})
	if err != nil {
		t.Fatal(err)
	}
	createTestUser(t, s, "requester-a", "collins")
	createTestUser(t, s, "requester-b", "collins")
	userA, err := s.GetUserByUsername(ctx, "requester-a")
	if err != nil {
		t.Fatal(err)
	}

	reqID, err := s.CreateFootageRequest(ctx, store.FootageRequest{CustomerID: 1, SiteID: siteID, RequestedBy: userA.ID, RequesterName: "requester-a", CamerasJSON: "[]", IncidentDate: "2026-01-01", StartTime: "10:00", EndTime: "11:00"})
	if err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	jobID, err := s.CreateJob(ctx, store.Job{NVRID: nvrID, CaseRef: "REQ-A", Destination: dest, RecordingsJSON: `[{"channel_id":"101","name":"Front","playback_uri":"x"}]`})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkRequestJob(ctx, reqID, jobID); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListJobItems(ctx, jobID)
	if err != nil || len(items) == 0 {
		t.Fatalf("ListJobItems = %v, %v", items, err)
	}
	outputPath := filepath.Join(dest, "front.mp4")
	if err := os.WriteFile(outputPath, []byte("fake video bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateJobItem(ctx, items[0].ID, "completed", 100, 17, 17, "", outputPath); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateJob(ctx, jobID, "completed", 100, "", nil); err != nil {
		t.Fatal(err)
	}

	cookieA := login(t, server, "requester-a", "a-password-123")
	cookieB := login(t, server, "requester-b", "a-password-123")

	path := "/downloads/jobs/" + strconv.FormatInt(jobID, 10) + "/zip"
	requestA := httptest.NewRequest(http.MethodGet, path, nil)
	requestA.AddCookie(cookieA)
	responseA := httptest.NewRecorder()
	server.Handler.ServeHTTP(responseA, requestA)
	if responseA.Code != http.StatusOK || !strings.Contains(responseA.Header().Get("Content-Type"), "zip") {
		t.Fatalf("owner zip download = %d %q, want 200 zip", responseA.Code, responseA.Header().Get("Content-Type"))
	}

	requestB := httptest.NewRequest(http.MethodGet, path, nil)
	requestB.AddCookie(cookieB)
	responseB := httptest.NewRecorder()
	server.Handler.ServeHTTP(responseB, requestB)
	if responseB.Code != http.StatusForbidden {
		t.Fatalf("a different requester's zip download = %d, want 403", responseB.Code)
	}
}
