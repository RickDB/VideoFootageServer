package store

import (
	"context"
	"path/filepath"
	"testing"
)

// newRequestTestStore sets up a site/NVR/customer/user/job/request fixture
// shared by the LinkRequestJobs tests below.
func newRequestTestStore(t *testing.T) (*Store, int64, func(caseRef string) int64) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"), "unit-test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	siteID, err := s.CreateSite(ctx, Site{Customer: "Acme", Name: "HQ", Timezone: "UTC", CustomerID: 1})
	if err != nil {
		t.Fatal(err)
	}
	nvrID, err := s.SaveNVR(ctx, NVR{SiteID: siteID, Name: "NVR1", Host: "192.0.2.5", Port: 80, Scheme: "http"})
	if err != nil {
		t.Fatal(err)
	}
	userID, err := s.CreateUserFull(ctx, User{Username: "reviewer", PasswordHash: "x", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	reqID, err := s.CreateFootageRequest(ctx, FootageRequest{CustomerID: 1, SiteID: siteID, RequestedBy: userID, RequesterName: "reviewer", CamerasJSON: "[]", IncidentDate: "2026-01-01", StartTime: "10:00", EndTime: "11:00"})
	if err != nil {
		t.Fatal(err)
	}

	makeJob := func(caseRef string) int64 {
		id, err := s.CreateJob(ctx, Job{NVRID: nvrID, CaseRef: caseRef, Destination: "/exports/x", RecordingsJSON: `[{"channel_id":"101","name":"Front","playback_uri":"x"}]`})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	return s, reqID, makeJob
}

func TestLinkRequestJobsRecordsEveryJobAndSetsPrimary(t *testing.T) {
	s, reqID, makeJob := newRequestTestStore(t)
	ctx := context.Background()
	job1 := makeJob("REQ-1")
	job2 := makeJob("REQ-1")

	if err := s.LinkRequestJobs(ctx, reqID, []int64{job1, job2}, "reviewer", "Auto-queued 2 jobs."); err != nil {
		t.Fatal(err)
	}

	req, err := s.GetFootageRequest(ctx, reqID)
	if err != nil {
		t.Fatal(err)
	}
	if req.Status != "retrieving" {
		t.Fatalf("status = %q, want retrieving", req.Status)
	}
	if !req.JobID.Valid || req.JobID.Int64 != job1 {
		t.Fatalf("primary job_id = %+v, want %d", req.JobID, job1)
	}
	if req.ReviewedBy != "reviewer" || req.ReviewerNotes != "Auto-queued 2 jobs." {
		t.Fatalf("reviewer fields not set: %+v", req)
	}

	jobs, err := s.ListJobsForRequest(ctx, reqID)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("ListJobsForRequest returned %d jobs, want 2", len(jobs))
	}
	if jobs[0].ID != job1 || jobs[1].ID != job2 {
		t.Fatalf("unexpected job set/order: %+v", jobs)
	}
}

func TestLinkRequestJobSingularStaysBackwardCompatible(t *testing.T) {
	s, reqID, makeJob := newRequestTestStore(t)
	ctx := context.Background()
	job := makeJob("REQ-2")

	// The manual "Link retrieval job" flow doesn't pass reviewer info — it
	// should still advance status and record the link without touching
	// reviewed_by/reviewer_notes.
	if err := s.LinkRequestJob(ctx, reqID, job); err != nil {
		t.Fatal(err)
	}
	req, err := s.GetFootageRequest(ctx, reqID)
	if err != nil {
		t.Fatal(err)
	}
	if req.Status != "retrieving" || !req.JobID.Valid || req.JobID.Int64 != job {
		t.Fatalf("unexpected request state: %+v", req)
	}
	if req.ReviewedBy != "" || req.ReviewerNotes != "" {
		t.Fatalf("manual link should not set reviewer fields: %+v", req)
	}
	jobs, err := s.ListJobsForRequest(ctx, reqID)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != job {
		t.Fatalf("ListJobsForRequest = %+v, want just %d", jobs, job)
	}
}

func TestLinkRequestJobsRejectsEmptyList(t *testing.T) {
	s, reqID, _ := newRequestTestStore(t)
	if err := s.LinkRequestJobs(context.Background(), reqID, nil, "reviewer", "note"); err == nil {
		t.Fatal("expected an error linking zero jobs")
	}
}
