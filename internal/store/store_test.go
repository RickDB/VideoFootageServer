package store

import (
	"context"
	"path/filepath"
	"testing"
)

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
