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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RickDB/HikFootageServer/internal/config"
	"github.com/RickDB/HikFootageServer/internal/recorder"
	"github.com/RickDB/HikFootageServer/internal/safepath"
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
	if e = m.store.EnsureJobItems(ctx, j); e != nil {
		fail(e, nil)
		return
	}
	items, e := m.store.ListJobItems(ctx, j.ID)
	if e != nil || len(items) == 0 {
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
	c, e := recorder.New(nvr, 24*time.Hour)
	if e != nil {
		fail(e, nil)
		return
	}
	_ = m.store.UpdateJob(ctx, j.ID, "downloading", j.Progress, "", nil)
	for _, item := range items {
		if item.Status == "completed" {
			continue
		}
		if e = m.processItem(ctx, c, j, item, dest); e != nil {
			m.log.Error("camera retrieval failed", "job", j.ID, "camera", item.CameraName, "error", e)
		}
		_, _, _ = m.store.RecalculateJobProgress(ctx, j.ID)
	}
	items, e = m.store.ListJobItems(ctx, j.ID)
	if e != nil {
		fail(e, nil)
		return
	}
	files := []string{}
	manifest := Manifest{JobID: j.ID, CaseReference: j.CaseRef, NVR: nvr.Name, CreatedAt: j.CreatedAt}
	failed := 0
	var failures []string
	for _, item := range items {
		if item.Status == "failed" {
			failed++
			failures = append(failures, item.CameraName+": "+item.Error)
			continue
		}
		if item.Status != "completed" {
			continue
		}
		var r store.Recording
		if json.Unmarshal([]byte(item.RecordingJSON), &r) != nil {
			continue
		}
		digest, size, hashErr := hashFile(item.OutputPath)
		if hashErr != nil {
			failed++
			failures = append(failures, item.CameraName+": "+hashErr.Error())
			_ = m.store.UpdateJobItem(ctx, item.ID, "failed", item.Progress, item.BytesDownloaded, item.TotalBytes, hashErr.Error(), item.OutputPath)
			continue
		}
		files = append(files, item.OutputPath)
		manifest.Files = append(manifest.Files, ManifestFile{Name: filepath.Base(item.OutputPath), SHA256: digest, Bytes: size, ChannelID: r.ChannelID, RequestedStart: r.Start, RequestedEnd: r.End, SourceURI: r.PlaybackURI})
	}
	_ = m.store.UpdateJob(ctx, j.ID, "verifying", 95, "", files)
	b, _ := json.MarshalIndent(manifest, "", "  ")
	manifestName := "manifest.json"
	if j.DestinationIsFinal {
		manifestName = fmt.Sprintf("manifest-job-%d.json", j.ID)
	}
	manifestPath := filepath.Join(dest, manifestName)
	if e = os.WriteFile(manifestPath, b, 0o640); e != nil {
		fail(e, files)
		return
	}
	files = append(files, manifestPath)
	if failed > 0 {
		progress, _, _ := m.store.RecalculateJobProgress(ctx, j.ID)
		_ = m.store.UpdateJob(ctx, j.ID, "failed", progress, strings.Join(failures, "; "), files)
		m.log.Warn("export completed with failures", "job", j.ID, "failed_items", failed)
		return
	}
	if e = m.store.UpdateJob(ctx, j.ID, "completed", 100, "", files); e != nil {
		m.log.Error("save completed status", "job", j.ID, "error", e)
	} else {
		m.log.Info("export completed", "job", j.ID, "files", len(files))
	}
}

func (m *Manager) processItem(ctx context.Context, c recorder.Client, j store.Job, item store.JobItem, dest string) error {
	var recording store.Recording
	if err := json.Unmarshal([]byte(item.RecordingJSON), &recording); err != nil {
		_ = m.store.UpdateJobItem(ctx, item.ID, "failed", 0, 0, item.TotalBytes, err.Error(), "")
		return err
	}
	resp, err := c.OpenDownload(ctx, recording.PlaybackURI)
	if err != nil {
		_ = m.store.UpdateJobItem(ctx, item.ID, "failed", 0, 0, item.TotalBytes, err.Error(), "")
		return err
	}
	defer resp.Body.Close()
	total := resp.ContentLength
	if total <= 0 {
		total = item.TotalBytes
	}
	cameraName := recording.Name
	if cameraName == "" {
		cameraName = item.CameraName
	}
	if cameraName == "" {
		cameraName = "Camera " + recording.ChannelID
	}
	base := safepath.Component(cameraName, "Camera") + "_" + safepath.WallTimecode(recording.Start)
	raw := filepath.Join(dest, base+".dav")
	f, err := os.OpenFile(raw, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		_ = m.store.UpdateJobItem(ctx, item.ID, "failed", 0, 0, total, err.Error(), "")
		return err
	}
	tracker := &progressTracker{total: total, last: time.Now().Add(-time.Second), update: func(written int64, progress int) {
		_ = m.store.UpdateJobItem(context.Background(), item.ID, "downloading", progress, written, total, "", "")
		_, _, _ = m.store.RecalculateJobProgress(context.Background(), j.ID)
	}}
	_, copyErr := io.Copy(io.MultiWriter(f, tracker), resp.Body)
	closeErr := f.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = m.store.UpdateJobItem(context.Background(), item.ID, "failed", totalProgress(tracker.written, total), tracker.written, total, copyErr.Error(), raw)
		return copyErr
	}
	_ = m.store.UpdateJobItem(ctx, item.ID, "remuxing", 90, tracker.written, total, "", raw)
	out := raw
	if m.cfg.FFmpegPath != "" {
		mp4 := filepath.Join(dest, base+".mp4")
		tmp := mp4 + ".tmp"
		diagPath := filepath.Join(dest, base+".ffmpeg-diagnostics.txt")
		_ = os.Remove(tmp)
		// Larger probesize/analyzeduration than ffmpeg's defaults: recorder-native
		// .dav headers occasionally confuse format auto-detection on short clips,
		// which previously produced an mp4 that "succeeded" (exit 0) but decoded
		// zero frames. Writing to a .tmp path and only renaming into place after
		// ffprobe confirms it's decodable means a half-written or invalid file
		// never appears at the final .mp4 path, even under a crash or a
		// concurrent read of the destination folder.
		cmd := exec.CommandContext(ctx, m.cfg.FFmpegPath, "-hide_banner", "-loglevel", "error", "-y",
			"-probesize", "50M", "-analyzeduration", "50M", "-i", raw, "-map", "0", "-c", "copy", tmp)
		output, runErr := cmd.CombinedOutput()
		switch {
		case runErr != nil:
			m.log.Warn("ffmpeg remux failed; preserving recorder-native file", "job", j.ID, "camera", item.CameraName, "error", runErr, "output", string(output))
			writeDiagnostics(diagPath, "ffmpeg exited with an error", runErr, output)
			_ = os.Remove(tmp)
		case !m.validVideoFile(ctx, tmp):
			// ffmpeg exited 0 but the output is not a decodable video (e.g. an
			// empty/near-empty file). Never trust exit code alone here.
			m.log.Warn("ffmpeg produced an unplayable mp4; preserving recorder-native file", "job", j.ID, "camera", item.CameraName, "output", string(output))
			writeDiagnostics(diagPath, "ffmpeg exited 0 but ffprobe rejected the output", nil, output)
			_ = os.Remove(tmp)
		default:
			if renameErr := os.Rename(tmp, mp4); renameErr != nil {
				m.log.Warn("could not finalize remuxed mp4; preserving recorder-native file", "job", j.ID, "camera", item.CameraName, "error", renameErr)
				writeDiagnostics(diagPath, "rename to final path failed", renameErr, output)
				_ = os.Remove(tmp)
				break
			}
			_ = os.Remove(diagPath) // clear any diagnostics left by an earlier retry
			out = mp4
			if !m.cfg.PreserveRaw {
				_ = os.Remove(raw)
			}
		}
	}
	if _, _, err = hashFile(out); err != nil {
		_ = m.store.UpdateJobItem(ctx, item.ID, "failed", 95, tracker.written, total, err.Error(), out)
		return err
	}
	return m.store.UpdateJobItem(ctx, item.ID, "completed", 100, tracker.written, total, "", out)
}

// writeDiagnostics captures why a remux attempt was discarded, next to the
// recorder-native file it fell back to, so an operator troubleshooting a
// customer's report of a missing/broken export doesn't have to reproduce the
// failure to see what ffmpeg said.
func writeDiagnostics(path, reason string, runErr error, output []byte) {
	var b strings.Builder
	b.WriteString("HikFootageServer remux diagnostics\n")
	b.WriteString("Generated: " + time.Now().UTC().Format(time.RFC3339) + "\n")
	b.WriteString("Reason: " + reason + "\n")
	if runErr != nil {
		b.WriteString("Error: " + runErr.Error() + "\n")
	}
	b.WriteString("--- ffmpeg output ---\n")
	b.Write(output)
	b.WriteString("\n")
	_ = os.WriteFile(path, []byte(b.String()), 0o640)
}

// validVideoFile confirms the remuxed file actually contains a decodable
// video stream with a positive duration. Falls back to a size sanity check
// when ffprobe isn't available so a missing/misconfigured ffprobe path never
// blocks the whole pipeline.
func (m *Manager) validVideoFile(ctx context.Context, path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.Size() < 1024 {
		return false
	}
	if m.cfg.FFprobePath == "" {
		return true
	}
	cmd := exec.CommandContext(ctx, m.cfg.FFprobePath, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=codec_type", "-show_entries", "format=duration", "-of", "json", path)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	var probe struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if json.Unmarshal(out, &probe) != nil || len(probe.Streams) == 0 {
		return false
	}
	duration, _ := strconv.ParseFloat(probe.Format.Duration, 64)
	return duration > 0
}

type progressTracker struct {
	written, total int64
	last           time.Time
	update         func(int64, int)
}

func (p *progressTracker) Write(data []byte) (int, error) {
	n := len(data)
	p.written += int64(n)
	now := time.Now()
	if now.Sub(p.last) >= 500*time.Millisecond {
		p.last = now
		p.update(p.written, totalProgress(p.written, p.total))
	}
	return n, nil
}
func totalProgress(written, total int64) int {
	if total <= 0 {
		return 5
	}
	value := int(written * 85 / total)
	if value > 85 {
		return 85
	}
	if value < 1 && written > 0 {
		return 1
	}
	return value
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
		if j.DestinationIsFinal {
			return filepath.Clean(chosen), nil
		}
		return filepath.Join(chosen, fmt.Sprintf("%s-job-%d", safeName(j.CaseRef), j.ID)), nil
	}
	p := filepath.Join(root, chosen)
	if !j.DestinationIsFinal {
		p = filepath.Join(root, safeName(chosen), fmt.Sprintf("%s-job-%d", safeName(j.CaseRef), j.ID))
	}
	clean := filepath.Clean(p)
	if clean != root && !strings.HasPrefix(clean, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("destination escapes export root")
	}
	return clean, nil
}

func safeName(v string) string {
	return strings.ReplaceAll(safepath.Component(v, "export"), " ", "-")
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
