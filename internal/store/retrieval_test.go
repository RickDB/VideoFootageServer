package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestQueueHonorsPerSiteConcurrencyAndRetry(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "queue.db"), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	newSite := func(name string) (int64, int64) {
		siteID, err := s.CreateSite(ctx, Site{Customer: "Customer", Name: name, Timezone: "UTC", MaxConcurrentDownloads: 1})
		if err != nil {
			t.Fatal(err)
		}
		nvrID, err := s.SaveNVR(ctx, NVR{SiteID: siteID, Name: name + " NVR", Host: name + ".test", Port: 443, Scheme: "https", Username: "user", Password: "password", VerifyTLS: true})
		if err != nil {
			t.Fatal(err)
		}
		return siteID, nvrID
	}
	_, nvrA := newSite("site-a")
	_, nvrB := newSite("site-b")
	recordings, _ := json.Marshal([]Recording{{ChannelID: "101", Name: "Entrance", PlaybackURI: "/recording"}})
	newJob := func(nvrID int64, ref string) int64 {
		id, err := s.CreateJob(ctx, Job{NVRID: nvrID, CaseRef: ref, Destination: "test", RecordingsJSON: string(recordings)})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	firstA := newJob(nvrA, "A-1")
	secondA := newJob(nvrA, "A-2")
	firstB := newJob(nvrB, "B-1")

	job, err := s.NextQueuedJob(ctx)
	if err != nil || job.ID != firstA {
		t.Fatalf("first job = %d, %v; want %d", job.ID, err, firstA)
	}
	job, err = s.NextQueuedJob(ctx)
	if err != nil || job.ID != firstB {
		t.Fatalf("parallel job = %d, %v; want other site's %d", job.ID, err, firstB)
	}
	if err = s.UpdateJob(ctx, firstA, "failed", 25, "network", nil); err != nil {
		t.Fatal(err)
	}
	job, err = s.NextQueuedJob(ctx)
	if err != nil || job.ID != secondA {
		t.Fatalf("released site slot job = %d, %v; want %d", job.ID, err, secondA)
	}
	if err = s.RetryJob(ctx, firstA, "101"); err != nil {
		t.Fatal(err)
	}
	retried, err := s.GetJob(ctx, firstA)
	if err != nil || retried.Status != "queued" {
		t.Fatalf("retried status = %q, %v", retried.Status, err)
	}
	if _, err = s.DB.ExecContext(ctx, `UPDATE jobs SET destination_is_final=1 WHERE id=?`, firstA); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := s.GetJob(ctx, firstA)
	if err != nil || !roundTrip.DestinationIsFinal {
		t.Fatalf("final destination flag did not round trip: %+v, %v", roundTrip, err)
	}
}
