package jobs

import (
	"path/filepath"
	"testing"

	"github.com/RickDB/HikFootageServer/internal/config"
	"github.com/RickDB/HikFootageServer/internal/store"
)

func TestFinalDestinationUsesExactEvidenceFolder(t *testing.T) {
	root := t.TempDir()
	m := &Manager{cfg: config.Config{ExportRoot: root, AllowPrivateDestinations: true}}
	job := store.Job{ID: 42, CaseRef: "CASE-42", Destination: filepath.Join("site", "CASE-42 - 2026-09-01T14-00 to 2026-09-01T15-00"), DestinationIsFinal: true}
	got, err := m.destination(job)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, job.Destination)
	if got != want {
		t.Fatalf("destination = %q, want %q", got, want)
	}
}
