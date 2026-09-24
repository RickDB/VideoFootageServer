// Package monitor periodically pings each configured recorder to keep its
// online/offline status fresh on the dashboard, independent of the manual
// "Test & discover" action. Each site controls its own cadence.
package monitor

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/RickDB/HikFootageServer/internal/recorder"
	"github.com/RickDB/HikFootageServer/internal/store"
)

const (
	tickInterval       = time.Minute
	defaultPollMinutes = 10
	minPollMinutes     = 5
	pingTimeout        = 10 * time.Second
	maxConcurrentPolls = 8

	// Recording availability is checked far less often than the online/offline
	// ping: it needs a search call against the recorder rather than a cheap
	// device-info request, so hammering it at the same cadence as the status
	// poll would be unnecessarily heavy on recorders with many cameras.
	availabilityInterval     = 6 * time.Hour
	availabilityLookbackDays = 35
)

type Poller struct {
	store *store.Store
	log   *slog.Logger
}

func New(s *store.Store, l *slog.Logger) *Poller {
	return &Poller{store: s, log: l}
}

// Start runs the poller in the background until ctx is cancelled.
func (p *Poller) Start(ctx context.Context) {
	go p.run(ctx)
}

func (p *Poller) run(ctx context.Context) {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	availTicker := time.NewTicker(availabilityInterval)
	defer availTicker.Stop()
	p.tick(ctx)
	go p.tickAvailability(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.tick(ctx)
		case <-availTicker.C:
			p.tickAvailability(ctx)
		}
	}
}

func (p *Poller) tick(ctx context.Context) {
	sites, err := p.store.SitesForPolling(ctx)
	if err != nil {
		p.log.Warn("poller: list sites", "error", err)
		return
	}
	interval := make(map[int64]int, len(sites))
	for _, s := range sites {
		interval[s.ID] = s.PollIntervalMinutes
	}
	nvrs, err := p.store.ListNVRs(ctx)
	if err != nil {
		p.log.Warn("poller: list recorders", "error", err)
		return
	}
	now := time.Now()
	sem := make(chan struct{}, maxConcurrentPolls)
	var wg sync.WaitGroup
	for _, n := range nvrs {
		minutes := interval[n.SiteID]
		if minutes < minPollMinutes {
			minutes = defaultPollMinutes
		}
		if n.LastSeen.Valid && now.Sub(n.LastSeen.Time) < time.Duration(minutes)*time.Minute {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(n store.NVR) {
			defer wg.Done()
			defer func() { <-sem }()
			p.ping(ctx, n)
		}(n)
	}
	wg.Wait()
}

func (p *Poller) ping(ctx context.Context, n store.NVR) {
	status := "offline"
	client, err := recorder.New(n, pingTimeout)
	if err == nil {
		pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
		defer cancel()
		if _, derr := client.Device(pingCtx); derr == nil {
			status = "online"
		}
	}
	if err := p.store.UpdateNVRStatus(context.Background(), n.ID, status); err != nil {
		p.log.Warn("poller: update recorder status", "nvr", n.ID, "error", err)
	}
}

// tickAvailability runs the recording-availability check across every
// online recorder: it searches every enabled camera over a 35-day lookback
// window and records how many days back a recording still exists on
// whichever camera goes back furthest, distinct from a customer's
// secured-footage retention setting (which is about how long a completed
// retrieval job's output is kept, not what the recorder itself still
// holds).
func (p *Poller) tickAvailability(ctx context.Context) {
	nvrs, err := p.store.ListNVRs(ctx)
	if err != nil {
		p.log.Warn("poller: list recorders for availability check", "error", err)
		return
	}
	sem := make(chan struct{}, maxConcurrentPolls)
	var wg sync.WaitGroup
	for _, n := range nvrs {
		if n.Status == "offline" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(n store.NVR) {
			defer wg.Done()
			defer func() { <-sem }()
			p.checkAvailability(ctx, n)
		}(n)
	}
	wg.Wait()
}

func (p *Poller) checkAvailability(ctx context.Context, n store.NVR) {
	cams, err := p.store.ListCameras(ctx, n.ID)
	if err != nil || len(cams) == 0 {
		return
	}
	// Every camera on a site shares the same recorder but not the same
	// storage fate: a high-motion channel can fill its allocated retention
	// with far more, shorter segments than a quiet one and get overwritten
	// days sooner. Sampling just one channel (previously: whichever was
	// first) reported that one camera's retention as if it were the whole
	// recorder's — misleadingly showing "0d" for a site where most cameras
	// still held days of footage, just not the one that happened to be
	// checked. Search every enabled camera (falling back to every camera if
	// none are marked enabled) and report the best any of them still holds.
	var channels []string
	for _, c := range cams {
		if c.Enabled {
			channels = append(channels, c.ChannelID)
		}
	}
	if len(channels) == 0 {
		for _, c := range cams {
			channels = append(channels, c.ChannelID)
		}
	}
	client, err := recorder.New(n, pingTimeout)
	if err != nil {
		return
	}
	// Scales with camera count since this now searches every channel
	// instead of one — it only runs every 6h in the background under a
	// shared concurrency cap, so a slower recorder with several cameras
	// costing a couple of minutes here is not a concern.
	checkCtx, cancel := context.WithTimeout(ctx, time.Duration(20+15*len(channels))*time.Second)
	defer cancel()
	end := time.Now().UTC()
	start := end.AddDate(0, 0, -availabilityLookbackDays)
	recs, err := client.Search(checkCtx, channels, start, end)
	if err != nil {
		p.log.Warn("poller: recording availability search failed", "nvr", n.ID, "error", err)
		return
	}
	days := 0
	for _, rec := range recs {
		recStart, err := time.Parse(time.RFC3339, rec.Start)
		if err != nil {
			continue
		}
		if age := int(end.Sub(recStart).Hours() / 24); age > days {
			days = age
		}
	}
	if len(recs) == 0 {
		days = 0
	} else if days > availabilityLookbackDays {
		days = availabilityLookbackDays
	}
	if err := p.store.UpdateRecordingAvailability(context.Background(), n.ID, days); err != nil {
		p.log.Warn("poller: update recording availability", "nvr", n.ID, "error", err)
	}
}
