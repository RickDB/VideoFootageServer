// Package recorder is the manufacturer-agnostic recorder integration
// surface. Jobs, the web layer, and the background pollers talk to
// recorder.Client rather than any specific manufacturer's SDK/API, so
// supporting a new recorder family later means adding one adapter package
// that satisfies Client and a case in New — nothing else in the application
// changes.
package recorder

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/RickDB/HikFootageServer/internal/hikvision"
	"github.com/RickDB/HikFootageServer/internal/store"
)

type Client interface {
	Device(ctx context.Context) (hikvision.DeviceInfo, error)
	Channels(ctx context.Context) ([]store.Camera, error)
	Probe(ctx context.Context) (hikvision.DeviceInfo, []store.Camera, hikvision.Capabilities, error)
	Search(ctx context.Context, channelIDs []string, start, end time.Time) ([]store.Recording, error)
	OpenDownload(ctx context.Context, playbackURI string) (*http.Response, error)
}

const Hikvision = "Hikvision"

// Manufacturers lists every recorder family the New dispatcher currently
// supports, used to populate the manufacturer picker on the recorder form.
var Manufacturers = []string{Hikvision}

// New builds a Client for the given NVR's manufacturer. An empty
// Manufacturer is treated as Hikvision for backward compatibility with
// recorders configured before this field existed.
func New(n store.NVR, timeout time.Duration) (Client, error) {
	switch n.Manufacturer {
	case "", Hikvision:
		return hikvision.New(n, timeout)
	default:
		return nil, fmt.Errorf("unsupported recorder manufacturer %q", n.Manufacturer)
	}
}
