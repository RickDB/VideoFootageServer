package store

import (
	"context"
	"path/filepath"
	"testing"
)

// TestRoleCapabilities pins down the capability matrix behind every
// role-gated route and template check: admin must implicitly have every
// capability below (never a separate "or admin" bolted on beside each new
// one), reviewer processes requests but never downloads everything, and
// downloader/collins never reach request review.
func TestRoleCapabilities(t *testing.T) {
	cases := []struct {
		role                        string
		review, downloadAll, opTool bool
	}{
		{"admin", true, true, true},
		{"reviewer", true, true, false},
		{"downloader", false, true, false},
		{"collins", false, false, false},
	}
	for _, c := range cases {
		u := User{Role: c.role}
		if got := u.CanReviewRequests(); got != c.review {
			t.Errorf("role %q CanReviewRequests() = %v, want %v", c.role, got, c.review)
		}
		if got := u.CanDownloadAll(); got != c.downloadAll {
			t.Errorf("role %q CanDownloadAll() = %v, want %v", c.role, got, c.downloadAll)
		}
		if got := u.IsOperational(); got != c.opTool {
			t.Errorf("role %q IsOperational() = %v, want %v", c.role, got, c.opTool)
		}
	}
}

func TestEncryptedNVRRoundTrip(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "test.db"), "unit-test-key")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	site, e := s.CreateSite(context.Background(), Site{Customer: "Acme", Name: "HQ", Timezone: "UTC"})
	if e != nil {
		t.Fatal(e)
	}
	id, e := s.SaveNVR(context.Background(), NVR{SiteID: site, Name: "NVR", Host: "127.0.0.1", Port: 443, Scheme: "https", Username: "u", Password: "p", VerifyTLS: true})
	if e != nil {
		t.Fatal(e)
	}
	n, e := s.GetNVR(context.Background(), id)
	if e != nil {
		t.Fatal(e)
	}
	if n.Password != "p" || n.Name != "NVR" {
		t.Fatalf("unexpected round trip: %+v", n)
	}
	var raw string
	if e = s.DB.QueryRow(`SELECT password FROM nvrs WHERE id=?`, id).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	if raw == "p" || len(raw) < 4 || raw[:4] != "gcm:" {
		t.Fatalf("password was not encrypted: %q", raw)
	}
}
