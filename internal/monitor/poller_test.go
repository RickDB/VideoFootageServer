package monitor

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/RickDB/HikFootageServer/internal/store"
)

type fakeSearchDescription struct {
	TrackIDs []string `xml:"trackIDList>trackID"`
}

// fakeAvailabilityRecorder serves just enough ISAPI to exercise
// checkAvailability: a search response whose only recording is `age` days
// old, keyed by which track the request asked for.
func fakeAvailabilityRecorder(t *testing.T, ageByChannel map[string]int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ISAPI/ContentMgmt/search" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var desc fakeSearchDescription
		if err := xml.Unmarshal(body, &desc); err != nil || len(desc.TrackIDs) == 0 {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		track := desc.TrackIDs[0]
		age, ok := ageByChannel[track]
		w.Header().Set("Content-Type", "application/xml")
		if !ok {
			// No recordings for this channel: numOfMatches 0, empty list.
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><CMSearchResult><numOfMatches>0</numOfMatches><matchList></matchList></CMSearchResult>`)
			return
		}
		start := time.Now().UTC().AddDate(0, 0, -age).Format(time.RFC3339)
		end := time.Now().UTC().AddDate(0, 0, -age).Add(time.Minute).Format(time.RFC3339)
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><CMSearchResult><numOfMatches>1</numOfMatches><matchList><searchMatchItem><trackID>%s</trackID><timeSpan><startTime>%s</startTime><endTime>%s</endTime></timeSpan><mediaSegmentDescriptor><playbackURI>x</playbackURI><contentType>video</contentType><fileSize>1</fileSize></mediaSegmentDescriptor></searchMatchItem></matchList></CMSearchResult>`, track, start, end)
	}))
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func nvrFromTestServer(t *testing.T, srv *httptest.Server) store.NVR {
	t.Helper()
	u, err := neturl.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return store.NVR{Scheme: u.Scheme, Host: u.Hostname(), Port: port}
}

// TestCheckAvailabilityUsesBestCameraNotFirst pins down a real reporting
// bug: a site's cameras don't share retention just because they share a
// recorder — a high-motion channel can fill its storage allocation with
// far more/shorter segments and get overwritten days sooner than a quiet
// one. checkAvailability used to search only the first enabled camera and
// report that as if it were the whole recorder's availability, which could
// show e.g. "0d" for a recorder where most cameras still held days of
// footage — just not the one that happened to be picked. It must now
// search every enabled camera and report whichever goes back furthest.
func TestCheckAvailabilityUsesBestCameraNotFirst(t *testing.T) {
	// Channel 101 (checked first, alphabetically/insertion-order first) has
	// only 0-day-old footage; channel 201 goes back 3 days. The old
	// first-channel-only behavior would report 0d; the fix must report 3d.
	srv := fakeAvailabilityRecorder(t, map[string]int{"101": 0, "201": 3})
	defer srv.Close()

	s := testStore(t)
	ctx := context.Background()
	siteID, err := s.CreateSite(ctx, store.Site{Customer: "Acme", Name: "HQ", Timezone: "UTC", CustomerID: 1})
	if err != nil {
		t.Fatal(err)
	}
	nvr := nvrFromTestServer(t, srv)
	nvr.SiteID = siteID
	nvr.Name = "NVR1"
	nvrID, err := s.SaveNVR(ctx, nvr)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceCameras(ctx, nvrID, []store.Camera{
		{ChannelID: "101", Name: "D1", Enabled: true},
		{ChannelID: "201", Name: "D2", Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	saved, err := s.GetNVR(ctx, nvrID)
	if err != nil {
		t.Fatal(err)
	}

	p := New(s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p.checkAvailability(ctx, saved)

	updated, err := s.GetNVR(ctx, nvrID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.RecordingDaysAvailable != 3 {
		t.Fatalf("RecordingDaysAvailable = %d, want 3 (the best camera's availability, not just the first channel checked)", updated.RecordingDaysAvailable)
	}
}
