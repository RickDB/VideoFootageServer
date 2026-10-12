package compliance

import (
	"context"
	"encoding/json"
	"github.com/RickDB/HikFootageServer/internal/config"
	"github.com/RickDB/HikFootageServer/internal/store"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}
func TestScheduleCalendarAndDST(t *testing.T) {
	cases := []struct {
		name, mode, start, clock, after, want string
		interval                              int
		days                                  []int
	}{
		{"weekday", "weekly", "2026-10-12", "07:00", "2026-10-12T06:00:00Z", "2026-10-14T05:00:00Z", 2, []int{1, 3}},
		{"fortnight DST", "weekly", "2026-10-12", "07:00", "2026-10-14T05:00:00Z", "2026-10-26T06:00:00Z", 2, []int{1, 3}},
		{"daily DST", "daily", "2026-10-12", "07:00", "2026-10-24T05:30:00Z", "2026-10-25T06:00:00Z", 1, nil},
		{"missing spring time", "daily", "2026-03-28", "02:30", "2026-03-28T02:00:00Z", "2026-03-30T00:30:00Z", 1, nil},
		{"day interval", "daily", "2026-10-12", "07:00", "2026-10-13T01:00:00Z", "2026-10-15T05:00:00Z", 3, nil},
		{"future start", "weekly", "2026-10-14", "07:00", "2026-10-12T01:00:00Z", "2026-10-26T06:00:00Z", 2, []int{1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := Schedule{Name: "Check", Kind: "retention", Mode: c.mode, StartDate: c.start, StartTime: c.clock, Timezone: "Europe/Amsterdam", Interval: c.interval, Weekdays: c.days, AllSites: true}
			next, err := s.Next(utc(c.after))
			if err != nil || !next.Equal(utc(c.want)) {
				t.Fatalf("%v %v, wanted %s", next, err, c.want)
			}
		})
	}
	s := Schedule{Name: "Check", Kind: "retention", Mode: "weekly", StartDate: "2026-10-12", StartTime: "07:00", Timezone: "UTC", Interval: 1, AllSites: true}
	if s.Validate() == nil {
		t.Fatal("accepted empty weekdays")
	}
	s.Weekdays = []int{1, 1}
	if s.Validate() == nil {
		t.Fatal("accepted duplicate weekdays")
	}
	s.Weekdays = []int{1}
	s.Timezone = "invalid"
	if s.Validate() == nil {
		t.Fatal("accepted invalid timezone")
	}
}
func TestScheduledDispatchIsDurableAndDoesNotOverlap(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "db.sqlite"), "key")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	site, err := db.CreateSite(t.Context(), store.Site{Name: "KFC", CustomerID: 1, Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{ExportRoot: dir}
	m := New(db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := utc("2026-10-12T09:00:00Z")
	s := Schedule{Name: "Daily", Kind: "retention", Mode: "daily", StartDate: "2026-10-12", StartTime: "10:00", Timezone: "UTC", Interval: 1, AllSites: true, Active: true, Actor: "operator"}
	id, err := m.SaveSchedule(t.Context(), s, now)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = m.enqueueDue(t.Context(), utc("2026-10-12T10:00:00Z")); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := m.List(t.Context())
	if err != nil || len(runs) != 1 {
		t.Fatalf("%+v %v", runs, err)
	}
	if runs[0].ScheduleID != id || len(runs[0].SiteIDs) != 1 || runs[0].SiteIDs[0] != site {
		t.Fatalf("%+v", runs)
	}
	// A fresh manager reuses the persisted occurrence and cannot enqueue it twice.
	restarted := New(db, cfg, m.Log)
	if err = restarted.enqueueDue(t.Context(), utc("2026-10-12T10:00:00Z")); err != nil {
		t.Fatal(err)
	}
	if err = m.enqueueDue(t.Context(), utc("2026-10-13T10:00:00Z")); err != nil {
		t.Fatal(err)
	}
	runs, err = m.List(t.Context())
	if err != nil || len(runs) != 1 {
		t.Fatalf("overlap queued: %v %v", runs, err)
	}
	all, err := m.Schedules(t.Context())
	if err != nil || !strings.Contains(all[0].LastError, "still active") {
		t.Fatalf("%+v %v", all, err)
	}
	if _, err = db.DB.Exec("UPDATE compliance_runs SET status='completed'"); err != nil {
		t.Fatal(err)
	}
	// All-sites schedules include installations added after schedule creation.
	second, err := db.CreateSite(t.Context(), store.Site{Name: "New site", CustomerID: 1, Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.enqueueDue(t.Context(), utc("2026-10-14T12:00:00Z")); err != nil {
		t.Fatal(err)
	}
	runs, err = m.List(t.Context())
	if err != nil || len(runs) != 2 || len(runs[0].SiteIDs) != 2 {
		t.Fatalf("%+v %v", runs, err)
	}
	found := false
	for _, id := range runs[0].SiteIDs {
		if id == second {
			found = true
		}
	}
	if !found {
		t.Fatal("new site missing")
	}
	if err = m.ToggleSchedule(t.Context(), id, false, "operator", utc("2026-10-14T12:00:00Z")); err != nil {
		t.Fatal(err)
	}
	if err = m.enqueueDue(t.Context(), utc("2026-11-01T12:00:00Z")); err != nil {
		t.Fatal(err)
	}
	runs, err = m.List(t.Context())
	if err != nil || len(runs) != 2 {
		t.Fatal("paused schedule dispatched")
	}
}
func TestSiteSummaryIdentifiesWholeNVRAndIncompleteSites(t *testing.T) {
	base := Run{ID: 1, Kind: "retention", SiteIDs: []int64{1}, Status: "running", Targets: []Target{
		{SiteID: 1, NVRID: 10, Site: "KFC", Recorder: "NVR A", Channel: "101"},
		{SiteID: 1, NVRID: 10, Site: "KFC", Recorder: "NVR A", Channel: "201"},
		{SiteID: 1, NVRID: 20, Site: "KFC", Recorder: "NVR B", Channel: "101"},
	}}
	base.Results = []Result{{SiteID: 1, NVRID: 10, Site: "KFC", Recorder: "NVR A", Camera: "Entrance", Channel: "101", Status: "pass"}}
	summary := Summarize(base)
	if summary[0].Status != "pending" || summary[0].Pending != 2 {
		t.Fatalf("premature pass %+v", summary)
	}
	base.Results = append(base.Results, Result{SiteID: 1, NVRID: 10, Site: "KFC", Recorder: "NVR A", Camera: "Counter", Channel: "201", Status: "fail"}, Result{SiteID: 1, NVRID: 20, Site: "KFC", Recorder: "NVR B", Camera: "Kitchen", Channel: "101", Status: "fail"})
	summary = Summarize(base)
	if summary[0].Status != "fail" || summary[0].Recorders[0].Scope != "camera" || summary[0].Recorders[1].Scope != "entire_nvr" {
		t.Fatalf("%+v", summary)
	}
	base.Results[2].Status = "error"
	summary = Summarize(base)
	if summary[0].Recorders[1].Scope == "entire_nvr" {
		t.Fatal("network error treated as missing retention")
	}
	base.Results[1].Status = "pass"
	base.Results[2].Status = "pass"
	summary = Summarize(base)
	if summary[0].Status != "pass" {
		t.Fatalf("fully completed site should pass even while other sites run: %+v", summary)
	}
}
func TestOverviewKeepsLatestPerSiteAcrossSelectedRuns(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "db.sqlite"), "key")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, err := db.CreateSite(t.Context(), store.Site{Name: "A", CustomerID: 1, Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.CreateSite(t.Context(), store.Site{Name: "B", CustomerID: 1, Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	m := New(db, config.Config{ExportRoot: dir}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	save := func(sites []int64, results []Result) {
		r := Run{Kind: "retention", SiteIDs: sites, Created: time.Now(), Status: "completed", Results: results}
		data, _ := json.Marshal(r)
		if _, e := db.DB.Exec("INSERT INTO compliance_runs(data,status) VALUES(?,?)", string(data), r.Status); e != nil {
			t.Fatal(e)
		}
	}
	save([]int64{a, b}, []Result{{SiteID: a, Site: "A", Status: "pass", Recorder: "NVR"}, {SiteID: b, Site: "B", Status: "fail", Recorder: "NVR"}})
	save([]int64{a}, []Result{{SiteID: a, Site: "A", Status: "error", Recorder: "NVR"}})
	o, err := m.Overview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	states := map[int64]string{}
	for _, s := range o.Sites {
		if s.Kind == "retention" {
			states[s.SiteID] = s.Status
		}
	}
	if states[a] != "error" || states[b] != "fail" || len(o.History) != 2 {
		t.Fatalf("%+v", o)
	}
}

func TestEditedScheduleCannotDispatchStaleConfiguration(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "db.sqlite"), "key")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.CreateSite(t.Context(), store.Site{Name: "KFC", CustomerID: 1, Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	m := New(db, config.Config{ExportRoot: dir}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := utc("2026-10-12T09:00:00Z")
	schedule := Schedule{Name: "Original", Kind: "retention", Mode: "daily", StartDate: "2026-10-12", StartTime: "10:00", Timezone: "UTC", Interval: 1, AllSites: true, Active: true}
	id, err := m.SaveSchedule(t.Context(), schedule, now)
	if err != nil {
		t.Fatal(err)
	}
	before, err := m.Schedules(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	schedule.ID = id
	schedule.Kind = "snapshots"
	_, err = m.SaveSchedule(t.Context(), schedule, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.enqueueSchedule(t.Context(), before[0], utc("2026-10-12T10:00:00Z")); err != nil {
		t.Fatal(err)
	}
	runs, err := m.List(t.Context())
	if err != nil || len(runs) != 0 {
		t.Fatalf("stale schedule dispatched: %+v %v", runs, err)
	}
	if err = m.enqueueDue(t.Context(), utc("2026-10-12T10:00:00Z")); err != nil {
		t.Fatal(err)
	}
	runs, err = m.List(t.Context())
	if err != nil || len(runs) != 1 || runs[0].Kind != "snapshots" {
		t.Fatalf("%+v %v", runs, err)
	}
}
