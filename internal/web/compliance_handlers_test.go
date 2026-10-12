package web

import (
	"encoding/json"
	"github.com/RickDB/HikFootageServer/internal/compliance"
	"github.com/RickDB/HikFootageServer/internal/store"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestCompliancePageAndQueue(t *testing.T) {
	server, s := testServer(t)
	defer s.Close()
	cookie := loginAsOperator(t, server, s)
	_, err := s.CreateSite(t.Context(), store.Site{Name: "KFC Barendrecht", CustomerID: 1, Timezone: "Europe/Amsterdam"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/compliance", nil)
	req.AddCookie(cookie)
	out := httptest.NewRecorder()
	server.Handler.ServeHTTP(out, req)
	if out.Code != 200 || !strings.Contains(out.Body.String(), "KFC Barendrecht") {
		t.Fatalf("%d %s", out.Code, out.Body.String())
	}
	form := url.Values{"kind": {"snapshots"}, "scope": {"all"}, "reference": {"Annual"}}
	req = httptest.NewRequest("POST", "/compliance", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	out = httptest.NewRecorder()
	server.Handler.ServeHTTP(out, req)
	if out.Code != http.StatusSeeOther || out.Header().Get("Location") != "/compliance?run=1" {
		t.Fatalf("%d %s", out.Code, out.Body.String())
	}
	for _, path := range []string{"/api/compliance/1", "/api/compliance/1?format=csv", "/compliance?run=1"} {
		req = httptest.NewRequest("GET", path, nil)
		req.AddCookie(cookie)
		out = httptest.NewRecorder()
		server.Handler.ServeHTTP(out, req)
		if out.Code != 200 {
			t.Fatalf("%s: %d %s", path, out.Code, out.Body.String())
		}
	}
}
func TestComplianceRequiresAdmin(t *testing.T) {
	server, s := testServer(t)
	defer s.Close()
	cookie := loginAsOperator(t, server, s)
	_, err := s.DB.Exec("UPDATE users SET role='reviewer' WHERE username='operator'")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct{ method, path string }{{"GET", "/compliance"}, {"POST", "/compliance"}, {"POST", "/compliance/root"}, {"POST", "/compliance/1/repeat"}, {"GET", "/api/compliance/1"}} {
		req := httptest.NewRequest(entry.method, entry.path, nil)
		req.AddCookie(cookie)
		out := httptest.NewRecorder()
		server.Handler.ServeHTTP(out, req)
		if out.Code == 200 || out.Code == 500 {
			t.Fatalf("unauthorized %s: %d", entry.path, out.Code)
		}
	}
	var count int
	if err = s.DB.QueryRow("SELECT COUNT(*) FROM compliance_runs").Scan(&count); err != nil || count != 0 {
		t.Fatalf("unauthorized run: %d %v", count, err)
	}
}

func TestComplianceSelectionAndRepeat(t *testing.T) {
	server, s := testServer(t)
	defer s.Close()
	cookie := loginAsOperator(t, server, s)
	selected, err := s.CreateSite(t.Context(), store.Site{Name: "Selected", CustomerID: 1, Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateSite(t.Context(), store.Site{Name: "Other", CustomerID: 1, Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	send := func(path string, form url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		out := httptest.NewRecorder()
		server.Handler.ServeHTTP(out, req)
		return out
	}
	out := send("/compliance", url.Values{"kind": {"retention"}, "scope": {"selected"}, "site": {strconv.FormatInt(selected, 10)}, "reference": {"Annual"}})
	if out.Code != 303 {
		t.Fatalf("%d %s", out.Code, out.Body.String())
	}
	out = send("/compliance/1/repeat", url.Values{})
	if out.Code != 303 {
		t.Fatalf("repeat: %d %s", out.Code, out.Body.String())
	}
	for _, id := range []int{1, 2} {
		var data string
		if err = s.DB.QueryRow("SELECT data FROM compliance_runs WHERE id=?", id).Scan(&data); err != nil {
			t.Fatal(err)
		}
		var run compliance.Run
		if err = json.Unmarshal([]byte(data), &run); err != nil {
			t.Fatal(err)
		}
		if len(run.SiteIDs) != 1 || run.SiteIDs[0] != selected || run.Kind != "retention" {
			t.Fatalf("wrong selection: %+v", run)
		}
	}
	out = send("/compliance", url.Values{"kind": {"retention"}, "scope": {"selected"}})
	if out.Code != 400 {
		t.Fatalf("empty selection %d", out.Code)
	}
	out = send("/compliance/root", url.Values{"root": {"../escape"}})
	if out.Code != 400 {
		t.Fatalf("traversal %d", out.Code)
	}
}
