package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/RickDB/HikFootageServer/internal/store"
)

// seedApproveTestRequest creates a site/NVR/customer-1 footage request fixture
// and returns its ID, for the approveAndSecureHandler tests below.
func seedApproveTestRequest(t *testing.T, s *store.Store, camerasJSON, status string) int64 {
	t.Helper()
	ctx := context.Background()
	siteID, err := s.CreateSite(ctx, store.Site{Name: "HQ", CustomerID: 1, Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	reqID, err := s.CreateFootageRequest(ctx, store.FootageRequest{
		CustomerID: 1, SiteID: siteID, RequestedBy: 1, RequesterName: "someone",
		CamerasJSON: camerasJSON, IncidentDate: "2026-01-01", StartTime: "10:00", EndTime: "11:00",
		Status: status,
	})
	if err != nil {
		t.Fatal(err)
	}
	return reqID
}

func TestApproveAndSecureRequiresOperationalRole(t *testing.T) {
	server, s := testServer(t)
	defer s.Close()
	reqID := seedApproveTestRequest(t, s, "[]", "submitted")

	hash, err := bcrypt.GenerateFromPassword([]byte("a-password-123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUserFull(context.Background(), store.User{Username: "requester", PasswordHash: string(hash), Role: "collins", CustomerID: 1, AllSites: true}); err != nil {
		t.Fatal(err)
	}
	cookie := login(t, server, "requester", "a-password-123")

	form := url.Values{}
	request := httptest.NewRequest(http.MethodPost, "/requests/"+strconv.FormatInt(reqID, 10)+"/approve-and-secure", nil)
	request.PostForm = form
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/requests" {
		t.Fatalf("non-operational approve-and-secure: status=%d location=%q, want redirect to /requests", response.Code, response.Header().Get("Location"))
	}

	req, err := s.GetFootageRequest(context.Background(), reqID)
	if err != nil {
		t.Fatal(err)
	}
	if req.Status != "submitted" {
		t.Fatalf("request status changed to %q despite being blocked", req.Status)
	}
}

// loginAsOperator creates a fresh admin-role user without the bootstrap
// account's forced must-change-password flag (which would otherwise
// redirect every request except /account) and logs in as them.
func loginAsOperator(t *testing.T, server *http.Server, s *store.Store) *http.Cookie {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("a-password-123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUserFull(context.Background(), store.User{Username: "operator", PasswordHash: string(hash), Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	return login(t, server, "operator", "a-password-123")
}

func TestApproveAndSecureUnknownRequestIs404(t *testing.T) {
	server, s := testServer(t)
	defer s.Close()
	cookie := loginAsOperator(t, server, s)
	request := httptest.NewRequest(http.MethodPost, "/requests/999999/approve-and-secure", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestApproveAndSecureDeclinedRequestIsBlocked(t *testing.T) {
	server, s := testServer(t)
	defer s.Close()
	reqID := seedApproveTestRequest(t, s, "[]", "declined")
	cookie := loginAsOperator(t, server, s)

	request := httptest.NewRequest(http.MethodPost, "/requests/"+strconv.FormatInt(reqID, 10)+"/approve-and-secure", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want redirect", response.Code)
	}

	req, err := s.GetFootageRequest(context.Background(), reqID)
	if err != nil {
		t.Fatal(err)
	}
	if req.Status != "declined" {
		t.Fatalf("declined request status changed to %q", req.Status)
	}
	jobs, err := s.ListJobsForRequest(context.Background(), reqID)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected no jobs queued for a declined request, got %d", len(jobs))
	}
}

func TestApproveAndSecureNoCamerasIsHandledGracefully(t *testing.T) {
	server, s := testServer(t)
	defer s.Close()
	reqID := seedApproveTestRequest(t, s, "[]", "submitted")
	cookie := loginAsOperator(t, server, s)

	request := httptest.NewRequest(http.MethodPost, "/requests/"+strconv.FormatInt(reqID, 10)+"/approve-and-secure", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want redirect", response.Code)
	}

	req, err := s.GetFootageRequest(context.Background(), reqID)
	if err != nil {
		t.Fatal(err)
	}
	if req.Status != "submitted" {
		t.Fatalf("status changed to %q for a request with no cameras", req.Status)
	}
}

// TestApproveAndSecureLegacyCamerasFallBackToManualLink covers a request
// submitted before cameras_json carried a camera ID (the field
// approveAndSecureHandler needs to resolve which recorder a camera belongs
// to): it should redirect with a "couldn't resolve" notice and leave the
// request exactly as it was, rather than erroring or guessing.
func TestApproveAndSecureLegacyCamerasFallBackToManualLink(t *testing.T) {
	server, s := testServer(t)
	defer s.Close()
	// No "id" field at all — the shape a pre-upgrade request would have.
	reqID := seedApproveTestRequest(t, s, `[{"channel_id":"101","name":"Front door"}]`, "accepted")
	cookie := loginAsOperator(t, server, s)

	request := httptest.NewRequest(http.MethodPost, "/requests/"+strconv.FormatInt(reqID, 10)+"/approve-and-secure", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want redirect", response.Code)
	}

	req, err := s.GetFootageRequest(context.Background(), reqID)
	if err != nil {
		t.Fatal(err)
	}
	if req.Status != "accepted" {
		t.Fatalf("status changed to %q for unresolvable legacy cameras", req.Status)
	}
	jobs, err := s.ListJobsForRequest(context.Background(), reqID)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected no jobs queued when cameras can't be resolved, got %d", len(jobs))
	}
}

// TestRequestDetailRendersApproveAction exercises the request detail page
// template end to end (render errors are only logged, not surfaced as a
// non-200 response, so this checks the actual body) for both an operator,
// who should see the one-click action, and a declined request, which
// shouldn't offer it.
func TestRequestDetailRendersApproveAction(t *testing.T) {
	server, s := testServer(t)
	defer s.Close()
	cookie := loginAsOperator(t, server, s)

	submittedID := seedApproveTestRequest(t, s, "[]", "submitted")
	request := httptest.NewRequest(http.MethodGet, "/requests/"+strconv.FormatInt(submittedID, 10), nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "Approve &amp; secure footage") {
		t.Fatal("submitted request detail page should offer the approve-and-secure action for an operator")
	}

	declinedID := seedApproveTestRequest(t, s, "[]", "declined")
	request = httptest.NewRequest(http.MethodGet, "/requests/"+strconv.FormatInt(declinedID, 10), nil)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "Approve &amp; secure footage") {
		t.Fatal("declined request detail page should not offer the approve-and-secure action")
	}
}
