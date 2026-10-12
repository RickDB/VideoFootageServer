package web

import (
	"encoding/json"
	"github.com/RickDB/HikFootageServer/internal/compliance"
	"github.com/RickDB/HikFootageServer/internal/store"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestMonitoringSchedulingAndOverview(t *testing.T) {
	server, s := testServer(t)
	defer s.Close()
	cookie := loginAsOperator(t, server, s)
	site, err := s.CreateSite(t.Context(), store.Site{Name: "KFC Barendrecht", CustomerID: 1, Timezone: "Europe/Amsterdam"})
	if err != nil {
		t.Fatal(err)
	}
	post := func(path string, form url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		out := httptest.NewRecorder()
		server.Handler.ServeHTTP(out, req)
		return out
	}
	form := url.Values{"reference": {"Weekly GDPR"}, "kind": {"retention"}, "mode": {"weekly"}, "interval": {"2"}, "start_date": {"2026-10-12"}, "start_time": {"07:00"}, "timezone": {"Europe/Amsterdam"}, "weekday": {"1", "3"}, "scope": {"all"}, "active": {"on"}}
	out := post("/monitoring/schedules", form)
	if out.Code != 303 {
		t.Fatalf("%d %s", out.Code, out.Body.String())
	}
	req := httptest.NewRequest("GET", "/api/monitoring", nil)
	req.AddCookie(cookie)
	out = httptest.NewRecorder()
	server.Handler.ServeHTTP(out, req)
	var o compliance.Overview
	if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &o) != nil {
		t.Fatalf("%d %s", out.Code, out.Body.String())
	}
	if len(o.Schedules) != 1 || len(o.Sites) != 2 || o.Sites[0].SiteID != site || o.Schedules[0].Interval != 2 || len(o.Schedules[0].Weekdays) != 2 {
		t.Fatalf("%+v", o)
	}
	out = post("/monitoring/schedules/1/toggle", url.Values{"active": {"false"}})
	if out.Code != 303 {
		t.Fatal(out.Code, out.Body.String())
	}
	form.Set("reference", "Edited")
	form.Set("interval", "1")
	out = post("/monitoring/schedules/1", form)
	if out.Code != 303 {
		t.Fatal(out.Code, out.Body.String())
	}
	for _, path := range []string{"/monitoring", "/monitoring?schedule=1", "/compliance"} {
		req = httptest.NewRequest("GET", path, nil)
		req.AddCookie(cookie)
		out = httptest.NewRecorder()
		server.Handler.ServeHTTP(out, req)
		if out.Code != 200 || !strings.Contains(out.Body.String(), "<h1>Monitoring</h1>") || !strings.Contains(out.Body.String(), "mon-retention-chart") {
			t.Fatalf("%s %d %s", path, out.Code, out.Body.String())
		}
	}
	form.Del("weekday")
	out = post("/monitoring/schedules", form)
	if out.Code != 400 {
		t.Fatalf("empty weekdays accepted: %d", out.Code)
	}
	form.Set("weekday", "1")
	form.Set("interval", "0")
	out = post("/monitoring/schedules", form)
	if out.Code != 400 {
		t.Fatalf("zero interval accepted: %d", out.Code)
	}
	form.Set("interval", "1")
	form.Set("timezone", "Not/AZone")
	out = post("/monitoring/schedules", form)
	if out.Code != 400 {
		t.Fatalf("bad timezone accepted: %d", out.Code)
	}
	_, err = s.DB.Exec("UPDATE users SET role='reviewer' WHERE username='operator'")
	if err != nil {
		t.Fatal(err)
	}
	out = post("/monitoring/schedules/1/toggle", url.Values{"active": {"true"}})
	if out.Code == 200 || out.Code == 303 && out.Header().Get("Location") == "/monitoring#schedules" {
		t.Fatalf("reviewer changed schedule %d", out.Code)
	}
	req = httptest.NewRequest("GET", "/api/monitoring", nil)
	req.AddCookie(cookie)
	out = httptest.NewRecorder()
	server.Handler.ServeHTTP(out, req)
	if out.Code == 200 {
		t.Fatal("reviewer read overview")
	}
}
