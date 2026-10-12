package compliance

import (
	"bytes"
	"context"
	"fmt"
	"github.com/RickDB/HikFootageServer/internal/config"
	"github.com/RickDB/HikFootageServer/internal/recorder"
	"github.com/RickDB/HikFootageServer/internal/store"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeClient struct {
	recorder.Client
	search   func([]string, time.Time, time.Time) ([]store.Recording, error)
	snapshot func(string) ([]byte, string, error)
}

func (f fakeClient) Search(_ context.Context, ids []string, s, e time.Time) ([]store.Recording, error) {
	return f.search(ids, s, e)
}
func (f fakeClient) Snapshot(_ context.Context, ch string) ([]byte, string, error) {
	return f.snapshot(ch)
}
func TestDarkPlaceholder(t *testing.T) {
	im := image.NewRGBA(image.Rect(0, 0, 160, 120))
	if !DarkPlaceholder(im) {
		t.Fatal("black frame accepted")
	}
	for y := 45; y < 55; y++ {
		for x := 60; x < 100; x++ {
			im.Set(x, y, color.White)
		}
	}
	if !DarkPlaceholder(im) {
		t.Fatal("sparse logo accepted")
	}
	for y := 0; y < 120; y++ {
		for x := 0; x < 160; x++ {
			im.Set(x, y, color.RGBA{100, 150, 180, 255})
		}
	}
	if DarkPlaceholder(im) {
		t.Fatal("live scene skipped")
	}
}
func TestResolveRoot(t *testing.T) {
	cfg := config.Config{ExportRoot: t.TempDir()}
	if _, err := ResolveRoot(cfg, "../outside"); err == nil {
		t.Fatal("allowed traversal")
	}
	if _, err := ResolveRoot(cfg, t.TempDir()); err == nil {
		t.Fatal("allowed disabled absolute destination")
	}
	got, err := ResolveRoot(cfg, "gdpr")
	if err != nil || got != filepath.Join(cfg.ExportRoot, "gdpr") {
		t.Fatalf("%q %v", got, err)
	}
}
func TestRetentionExactLocalTarget(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	m := &Manager{}
	r := Run{Created: now}
	w := work{site: store.Site{Timezone: "Europe/Amsterdam"}, cam: store.Camera{ChannelID: "101"}}
	expected := time.Date(2026, 10, 2, 13, 0, 0, 0, time.UTC)
	for _, scenario := range []string{"pass", "fail", "error", "malformed"} {
		t.Run(scenario, func(t *testing.T) {
			client := fakeClient{search: func(ids []string, start, end time.Time) ([]store.Recording, error) {
				if len(ids) != 1 || ids[0] != "101" || !start.Equal(expected.Add(-time.Minute)) || !end.Equal(expected.Add(time.Minute)) {
					t.Fatalf("wrong target %v %v %v", ids, start, end)
				}
				if scenario == "error" {
					return nil, fmt.Errorf("offline")
				}
				if scenario == "fail" {
					return []store.Recording{{ChannelID: "101", Start: start.Format(time.RFC3339), End: expected.Add(-time.Second).Format(time.RFC3339), PlaybackURI: "rtsp://recording"}}, nil
				}
				if scenario == "malformed" {
					return []store.Recording{{ChannelID: "101", Start: "bad", End: "bad"}}, nil
				}
				return []store.Recording{{ChannelID: "101", Start: start.Format(time.RFC3339), End: end.Format(time.RFC3339), PlaybackURI: "rtsp://recording"}}, nil
			}}
			var v Result
			m.retention(t.Context(), client, w, &r, &v)
			want := scenario
			if want == "malformed" {
				want = "error"
			}
			if v.Status != want {
				t.Fatalf("%+v", v)
			}
		})
	}
}
func TestSurveyContinuesAndPersists(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "db.sqlite"), "key")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, err := s.CreateSite(t.Context(), store.Site{Name: "KFC Barendrecht", CustomerID: 1, Timezone: "Europe/Amsterdam"})
	if err != nil {
		t.Fatal(err)
	}
	nvr, err := s.SaveNVR(t.Context(), store.NVR{SiteID: site, Name: "Recorder", Host: "example.invalid", Port: 80, Scheme: "http", Username: "x", Password: "y"})
	if err != nil {
		t.Fatal(err)
	}
	err = s.ReplaceCameras(t.Context(), nvr, []store.Camera{{ChannelID: "101", Name: "Offline", Enabled: true}, {ChannelID: "201", Name: "Analog", Enabled: true}, {ChannelID: "301", Name: "Entrance", Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	m := New(s, config.Config{RequestTimeout: time.Second}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.NewClient = func(store.NVR, time.Duration) (recorder.Client, error) {
		return fakeClient{snapshot: func(ch string) ([]byte, string, error) {
			if ch == "101" {
				return nil, "", fmt.Errorf("camera unreachable")
			}
			im := image.NewRGBA(image.Rect(0, 0, 32, 32))
			if ch == "301" {
				for y := 0; y < 32; y++ {
					for x := 0; x < 32; x++ {
						im.Set(x, y, color.White)
					}
				}
			}
			var b bytes.Buffer
			_ = jpeg.Encode(&b, im, nil)
			return b.Bytes(), "image/jpeg", nil
		}}, nil
	}
	id, err := m.Create(t.Context(), Run{Kind: "snapshots", Reference: "Annual", Actor: "operator", Root: dir, SiteIDs: []int64{site}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := m.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	m.execute(t.Context(), &r)
	saved, err := m.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Done != 3 || saved.Total != 3 || saved.Status != "completed_with_issues" {
		t.Fatalf("%+v", saved)
	}
	counts := map[string]int{}
	for _, v := range saved.Results {
		counts[v.Status]++
		if v.Status == "exported" {
			if filepath.Base(filepath.Dir(v.Path)) != "KFC Barendrecht" {
				t.Fatal(v.Path)
			}
			if _, err := os.Stat(v.Path); err != nil {
				t.Fatal(err)
			}
		}
	}
	if counts["error"] != 1 || counts["skipped"] != 1 || counts["exported"] != 1 {
		t.Fatal(counts)
	}
	if _, err := os.Stat(filepath.Join(saved.Root, "report.json")); err != nil {
		t.Fatal(err)
	}
	audit, err := s.ListAudit(t.Context(), 10)
	if err != nil || len(audit) != 2 {
		t.Fatalf("audit %v %v", audit, err)
	}
}
