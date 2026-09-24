package hikvision

import (
	"context"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/RickDB/HikFootageServer/internal/store"
)

func TestDigestHeader(t *testing.T) {
	h, e := digestHeader(`Digest realm="NVR", nonce="abc123", qop="auth", algorithm=MD5`, "operator", "secret", "GET", "/ISAPI/System/deviceInfo")
	if e != nil {
		t.Fatal(e)
	}
	for _, want := range []string{`username="operator"`, `realm="NVR"`, `uri="/ISAPI/System/deviceInfo"`, `qop=auth`, `nc=00000001`} {
		if !strings.Contains(h, want) {
			t.Fatalf("header %q missing %q", h, want)
		}
	}
}
func TestXMLEscape(t *testing.T) {
	if got := xmlEscape(`rtsp://host/a?x=1&y=<two>`); got != `rtsp://host/a?x=1&amp;y=&lt;two&gt;` {
		t.Fatalf("unexpected XML escape: %s", got)
	}
}

func TestProxyChannelID(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"101", "1", true},
		{"401", "4", true},
		{"1201", "12", true},
		{"100", "", false}, // doesn't end in 01
		{"1", "", false},   // below the 101 floor
		{"abc", "", false}, // not numeric
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := proxyChannelID(c.in)
		if ok != c.wantOK || got != c.want {
			t.Errorf("proxyChannelID(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.wantOK)
		}
	}
}

// newTestClient points a Client at an httptest server without going through
// digest auth (the server under test doesn't challenge), mirroring how the
// real recorder responds once already authenticated.
func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	u, err := parseTestURL(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(store.NVR{Scheme: u.scheme, Host: u.host, Port: u.port}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

type testURL struct {
	scheme string
	host   string
	port   int
}

func parseTestURL(raw string) (testURL, error) {
	u, err := neturl.Parse(raw)
	if err != nil {
		return testURL{}, err
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		return testURL{}, err
	}
	return testURL{scheme: u.Scheme, host: u.Hostname(), port: port}, nil
}

// TestSnapshotFallsBackToInputProxyPicture pins down a real firmware quirk:
// some older Hikvision NVR firmware rejects the generic
// Streaming/channels/{id}/picture request for a channel that is itself an
// IP camera proxied through the NVR (statusCode 6 "Invalid XML Content"),
// even though the very same channel ID works for search/download. Snapshot
// must retry via the parallel ContentMgmt/InputProxy picture endpoint
// before giving up, rather than surfacing the primary failure directly.
func TestSnapshotFallsBackToInputProxyPicture(t *testing.T) {
	const badXMLBody = `<?xml version="1.0" encoding="UTF-8" ?><ResponseStatus version="1.0" xmlns="http://www.hikvision.com/ver20/XMLSchema"><requestURL>/ISAPI/Streaming/channels/101/picture</requestURL><statusCode>6</statusCode><statusString>Invalid XML Content</statusString><subStatusCode>badXmlContent</subStatusCode></ResponseStatus>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ISAPI/Streaming/channels/101/picture":
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(badXMLBody))
		case "/ISAPI/ContentMgmt/InputProxy/channels/1/picture":
			w.Header().Set("Content-Type", "image/jpeg")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("fake-jpeg-bytes"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	data, ct, err := c.Snapshot(context.Background(), "101")
	if err != nil {
		t.Fatalf("Snapshot() with fallback = error %v, want success via InputProxy fallback", err)
	}
	if string(data) != "fake-jpeg-bytes" || ct != "image/jpeg" {
		t.Fatalf("Snapshot() = (%q, %q), want the InputProxy fallback's response", data, ct)
	}
}

// TestSnapshotSkipsFallbackForNonProxyChannel makes sure the fallback is
// only attempted for a channel ID shaped like a proxied channel — trying it
// for a channel that plainly isn't one shouldn't matter functionally (the
// ContentMgmt/InputProxy endpoint would just also fail), but pinning this
// down documents the intent and keeps a future change honest about it.
func TestSnapshotSkipsFallbackForNonProxyChannel(t *testing.T) {
	fallbackHit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "InputProxy") {
			fallbackHit = true
		}
		http.Error(w, "nope", http.StatusBadRequest)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	if _, _, err := c.Snapshot(context.Background(), "100"); err == nil {
		t.Fatal("Snapshot() with a failing primary and no valid proxy shape should return an error")
	}
	if fallbackHit {
		t.Fatal("Snapshot() should not have attempted the InputProxy fallback for a non-proxy-shaped channel ID")
	}
}
