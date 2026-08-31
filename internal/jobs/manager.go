package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/RickDB/HikFootageServer/internal/config"
	"github.com/RickDB/HikFootageServer/internal/hikvision"
	"github.com/RickDB/HikFootageServer/internal/store"
)

type Manager struct {
	store *store.Store
	cfg   config.Config
	log   *slog.Logger
	wake  chan struct{}
	once  sync.Once
}
type Manifest struct {
	JobID         int64          `json:"job_id"`
	CaseReference string         `json:"case_reference"`
	NVR           string         `json:"nvr"`
	CreatedAt     time.Time      `json:"created_at"`
	Files         []ManifestFile `json:"files"`
}
type ManifestFile struct {
	Name, SHA256, ChannelID, RequestedStart, RequestedEnd, SourceURI string
	Bytes                                                            int64
}

func New(s *store.Store, c config.Config, l *slog.Logger) *Manager {
	return &Manager{store: s, cfg: c, log: l, wake: make(chan struct{}, 1)}
}
func (m *Manager) Start(ctx context.Context) {
	m.once.Do(func() {
		_ = m.store.RecoverJobs(ctx)
		for i := 0; i < m.cfg.MaxConcurrentExports; i++ {
			go m.worker(ctx, i)
		}
		m.Wake()
	})
}
func (m *Manager) Wake() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}
func (m *Manager) worker(ctx context.Context, id int) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.wake:
			for {
				j, e := m.store.NextQueuedJob(ctx)
				if e != nil {
					break
				}
				m.Wake()
				m.log.Info("export started", "job", j.ID, "worker", id)
				m.process(ctx, j)
			}
		}
	}
}

func (m *Manager) process(ctx context.Context, j store.Job) {
	fail := func(e error, files []string) {
		m.log.Error("export failed", "job", j.ID, "error", e)
		if errors.Is(e, context.Canceled) || ctx.Err() != nil {
			_ = m.store.UpdateJob(context.Background(), j.ID, "queued", 0, "Interrupted; queued for restart", files)
			return
		}
		_ = m.store.UpdateJob(context.Background(), j.ID, "failed", j.Progress, e.Error(), files)
	}
	nvr, e := m.store.GetNVR(ctx, j.NVRID)
	if e != nil {
		fail(e, nil)
		return
	}
	var recordings []store.Recording
	if e = json.Unmarshal([]byte(j.RecordingsJSON), &recordings); e != nil || len(recordings) == 0 {
		if e == nil {
			e = fmt.Errorf("job contains no recordings")
		}
		fail(e, nil)
		return
	}
	dest, e := m.destination(j)
	if e != nil {
		fail(e, nil)
		return
	}
	if e = os.MkdirAll(dest, 0o750); e != nil {
		fail(e, nil)
		return
	}
	c, e := hikvision.New(nvr, 24*time.Hour)
	if e != nil {
		fail(e, nil)
		return
	}
	files := []string{}
	manifest := Manifest{JobID: j.ID, CaseReference: j.CaseRef, NVR: nvr.Name, CreatedAt: j.CreatedAt}
	for i, r := range recordings {
		base := fmt.Sprintf("%02d_%s_%s", i+1, safeName(r.ChannelID), safeTime(r.Start))
		raw := filepath.Join(dest, base+".dav")
		resp, e := c.OpenDownload(ctx, r.PlaybackURI)
		if e != nil {
			fail(e, files)
			return
		}
		f, e := os.OpenFile(raw, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
		if e != nil {
			resp.Body.Close()
			fail(e, files)
			return
		}
		_, e = io.Copy(f, resp.Body)
		closeErr := f.Close()
		resp.Body.Close()
		if e == nil {
			e = closeErr
		}
		if e != nil {
			fail(e, files)
			return
		}
		out := raw
		if m.cfg.FFmpegPath != "" {
			mp4 := filepath.Join(dest, base+".mp4")
			cmd := exec.CommandContext(ctx, m.cfg.FFmpegPath, "-hide_banner", "-loglevel", "error", "-i", raw, "-map", "0", "-c", "copy", mp4)
			if output, runErr := cmd.CombinedOutput(); runErr == nil {
				out = mp4
				if !m.cfg.PreserveRaw {
					_ = os.Remove(raw)
				}
			} else {
				m.log.Warn("ffmpeg remux unavailable; preserving recorder-native file", "job", j.ID, "error", runErr, "output", string(output))
			}
		}
		digest, size, e := hashFile(out)
		if e != nil {
			fail(e, files)
			return
		}
		files = append(files, out)
		manifest.Files = append(manifest.Files, ManifestFile{Name: filepath.Base(out), SHA256: digest, Bytes: size, ChannelID: r.ChannelID, RequestedStart: r.Start, RequestedEnd: r.End, SourceURI: r.PlaybackURI})
		progress := 10 + int(float64(i+1)/float64(len(recordings))*80)
		_ = m.store.UpdateJob(ctx, j.ID, "downloading", progress, "", files)
	}
	_ = m.store.UpdateJob(ctx, j.ID, "verifying", 95, "", files)
	b, _ := json.MarshalIndent(manifest, "", "  ")
	manifestPath := filepath.Join(dest, "manifest.json")
	if e = os.WriteFile(manifestPath, b, 0o640); e != nil {
		fail(e, files)
		return
	}
	files = append(files, manifestPath)
	if e = m.store.UpdateJob(ctx, j.ID, "completed", 100, "", files); e != nil {
		m.log.Error("save completed status", "job", j.ID, "error", e)
	} else {
		m.log.Info("export completed", "job", j.ID, "files", len(files))
	}
}
func (m *Manager) destination(j store.Job) (string, error) {
	root, e := filepath.Abs(m.cfg.ExportRoot)
	if e != nil {
		return "", e
	}
	chosen := strings.TrimSpace(j.Destination)
	if chosen == "" {
		chosen = safeName(j.CaseRef)
	}
	if filepath.IsAbs(chosen) {
		if !m.cfg.AllowPrivateDestinations {
			return "", fmt.Errorf("absolute custom destinations are disabled")
		}
		return filepath.Join(chosen, fmt.Sprintf("%s-job-%d", safeName(j.CaseRef), j.ID)), nil
	}
	p := filepath.Join(root, safeName(chosen), fmt.Sprintf("%s-job-%d", safeName(j.CaseRef), j.ID))
	clean := filepath.Clean(p)
	if clean != root && !strings.HasPrefix(clean, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("destination escapes export root")
	}
	return clean, nil
}

var unsafe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func safeName(v string) string {
	v = strings.Trim(unsafe.ReplaceAllString(v, "-"), "-.")
	if v == "" {
		return "export"
	}
	return v
}
func safeTime(v string) string {
	if t, e := time.Parse(time.RFC3339, v); e == nil {
		return t.UTC().Format("20060102T150405Z")
	}
	return safeName(v)
}
func hashFile(path string) (string, int64, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", 0, e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, e
}
