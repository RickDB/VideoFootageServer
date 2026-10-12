package compliance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/RickDB/HikFootageServer/internal/config"
	"github.com/RickDB/HikFootageServer/internal/recorder"
	"github.com/RickDB/HikFootageServer/internal/safepath"
	"github.com/RickDB/HikFootageServer/internal/store"
)

type Result struct {
	NVRID                                                         int64
	SiteID                                                        int64
	Site, Recorder, Camera, Channel, Status, Detail, Path, Target string
	At                                                            time.Time
}
type Run struct {
	ScheduleID                                  int64
	ScheduledFor                                time.Time
	Targets                                     []Target
	Current                                     string
	ID                                          int64
	Kind, Reference, Actor, Root, Status, Error string
	SiteIDs                                     []int64
	Created                                     time.Time
	Done, Total                                 int
	Results                                     []Result
}
type Manager struct {
	Store     *store.Store
	Config    config.Config
	Log       *slog.Logger
	NewClient func(store.NVR, time.Duration) (recorder.Client, error)
}

func New(s *store.Store, c config.Config, l *slog.Logger) *Manager {
	return &Manager{s, c, l, recorder.New}
}
func (m *Manager) Create(ctx context.Context, r Run) (int64, error) {
	r.Created = time.Now().UTC()
	r.Status = "queued"
	b, err := json.Marshal(r)
	if err != nil {
		return 0, err
	}
	tx, err := m.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "INSERT INTO compliance_runs(data,status) VALUES(?,?)", string(b), r.Status)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO audit_log(actor,action,entity_type,entity_id,details) VALUES(?,?,?,?,?)", r.Actor, "compliance.created", "compliance_run", id, string(b))
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}
func (m *Manager) Get(ctx context.Context, id int64) (Run, error) {
	var r Run
	var data, status string
	err := m.Store.DB.QueryRowContext(ctx, "SELECT data,status FROM compliance_runs WHERE id=?", id).Scan(&data, &status)
	if err != nil {
		return r, err
	}
	err = json.Unmarshal([]byte(data), &r)
	r.ID = id
	r.Status = status
	return r, err
}
func (m *Manager) List(ctx context.Context) ([]Run, error) {
	rows, err := m.Store.DB.QueryContext(ctx, "SELECT id,data,status FROM compliance_runs ORDER BY id DESC LIMIT 100")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		var r Run
		var data, status string
		var id int64
		if err = rows.Scan(&id, &data, &status); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(data), &r); err != nil {
			return nil, err
		}
		r.ID = id
		r.Status = status
		out = append(out, r)
	}
	return out, rows.Err()
}
func (m *Manager) save(r *Run) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = m.Store.DB.Exec("UPDATE compliance_runs SET data=?,status=? WHERE id=?", string(b), r.Status, r.ID)
	return err
}
func (m *Manager) Start(ctx context.Context) {
	go func() {
		if _, err := m.Store.DB.ExecContext(ctx, "UPDATE compliance_runs SET status='interrupted' WHERE status='running'"); err != nil {
			m.Log.Error("recover compliance runs", "error", err)
			return
		}
		go m.scheduleLoop(ctx)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			if ctx.Err() != nil {
				return
			}
			var id int64
			err := m.Store.DB.QueryRowContext(ctx, "SELECT id FROM compliance_runs WHERE status='queued' ORDER BY id LIMIT 1").Scan(&id)
			if err == nil {
				r, e := m.Get(ctx, id)
				if e == nil {
					m.execute(ctx, &r)
				}
				continue
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
func (m *Manager) append(r *Run, v Result) error {
	v.At = time.Now().UTC()
	r.Results = append(r.Results, v)
	r.Done++
	if v.Status == "error" {
		m.Log.Warn("compliance camera failed", "run", r.ID, "site", v.Site, "recorder", v.Recorder, "channel", v.Channel, "detail", v.Detail)
	}
	return m.save(r)
}

type work struct {
	discover bool
	site     store.Site
	nvr      store.NVR
	cam      store.Camera
	err      error
}

func (m *Manager) execute(ctx context.Context, r *Run) {
	r.Status = "running"
	if err := m.save(r); err != nil {
		m.Log.Error("save compliance run", "error", err)
		return
	}
	fail := func(err error) {
		r.Status = "failed"
		r.Error = err.Error()
		if e := m.save(r); e != nil {
			m.Log.Error("save failed compliance run", "error", e)
		}
	}
	var todo []work
	for _, id := range r.SiteIDs {
		s, err := m.Store.GetSite(ctx, id)
		if err != nil {
			todo = append(todo, work{site: store.Site{ID: id, Name: fmt.Sprint("Site ", id)}, err: err})
			continue
		}
		ns, err := m.Store.ListNVRsBySite(ctx, id)
		if err != nil || len(ns) == 0 {
			if err == nil {
				err = fmt.Errorf("no recorders configured")
			}
			todo = append(todo, work{site: s, err: err})
			continue
		}
		for _, n := range ns {
			cams, err := m.Store.ListCameras(ctx, n.ID)
			if err != nil {
				todo = append(todo, work{site: s, nvr: n, err: err})
				continue
			}
			if len(cams) == 0 {
				todo = append(todo, work{site: s, nvr: n, discover: true})
				continue
			}
			count := 0
			for _, c := range cams {
				if c.Enabled {
					todo = append(todo, work{site: s, nvr: n, cam: c})
					count++
				}
			}
			if count == 0 {
				todo = append(todo, work{site: s, nvr: n, err: fmt.Errorf("all discovered cameras are disabled; enable the required channels")})
			}
		}
	}
	r.Total = len(todo)
	r.Targets = targets(todo)
	r.Root = filepath.Join(r.Root, fmt.Sprintf("%s-%s-%d", safepath.Component(r.Reference, "survey"), r.Created.Format("20060102T150405Z"), r.ID))
	if err := os.MkdirAll(r.Root, 0750); err != nil {
		fail(err)
		return
	}
	if err := m.save(r); err != nil {
		fail(err)
		return
	}
	names := map[int64]string{}
	used := map[string]bool{}
	for _, w := range todo {
		if _, ok := names[w.site.ID]; ok {
			continue
		}
		base := safepath.Component(w.site.Name, "site")
		name := base
		for suffix := 0; used[strings.ToLower(name)]; suffix++ {
			name = fmt.Sprintf("%s (%d-%d)", base, w.site.ID, suffix)
		}
		used[strings.ToLower(name)] = true
		names[w.site.ID] = name
	}
	for i := 0; i < len(todo); i++ {
		w := todo[i]
		if ctx.Err() != nil {
			r.Status = "interrupted"
			r.Error = "Server stopped; start a new run to retry."
			_ = m.save(r)
			return
		}

		if w.discover {
			r.Current = "Discovering cameras: " + w.site.Name + " / " + w.nvr.Name
			if err := m.save(r); err != nil {
				fail(err)
				return
			}
			cams, err := m.discoverCameras(ctx, w.nvr)
			if err != nil {
				w.err = fmt.Errorf("camera discovery failed: %w", err)
			} else {
				var expanded []work
				for _, cam := range cams {
					if cam.Enabled {
						expanded = append(expanded, work{site: w.site, nvr: w.nvr, cam: cam})
					}
				}
				if len(expanded) == 0 {
					w.err = fmt.Errorf("recorder discovery returned no enabled cameras")
				} else {
					// Replace this recorder placeholder with its actual camera work.
					r.Total += len(expanded) - 1
					todo = append(todo[:i], append(expanded, todo[i+1:]...)...)
					r.Targets = targets(todo)
					i--
					continue
				}
			}
		}
		r.Current = w.site.Name + " / " + w.nvr.Name + " / " + w.cam.Name
		if err := m.save(r); err != nil {
			fail(err)
			return
		}
		v := Result{SiteID: w.site.ID, NVRID: w.nvr.ID, Site: w.site.Name, Recorder: w.nvr.Name, Camera: w.cam.Name, Channel: w.cam.ChannelID}
		if w.err != nil {
			v.Status = "error"
			v.Detail = w.err.Error()
		} else {
			timeout := m.Config.RequestTimeout
			if timeout <= 0 {
				timeout = 30 * time.Second
			}
			client, err := m.NewClient(w.nvr, timeout)
			if err != nil {
				v.Status = "error"
				v.Detail = err.Error()
			} else {
				checkCtx, cancel := context.WithTimeout(ctx, timeout*3)
				if r.Kind == "snapshots" {
					m.snapshot(checkCtx, client, w, r, names[w.site.ID], &v)
				} else {
					m.retention(checkCtx, client, w, r, &v)
				}
				cancel()
			}
		}
		if err := m.append(r, v); err != nil {
			fail(err)
			return
		}
	}
	r.Current = ""
	r.Status = "completed"
	for _, v := range r.Results {
		if v.Status == "error" || v.Status == "fail" {
			r.Status = "completed_with_issues"
			break
		}
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(r.Root, "report.json"), b, 0640)
	}
	if err != nil {
		fail(fmt.Errorf("write report: %w", err))
		return
	}
	if err = m.save(r); err != nil {
		m.Log.Error("finish compliance run", "run", r.ID, "error", err)
	}
	if err = m.Store.LogAudit(context.Background(), r.Actor, "compliance."+r.Status, "compliance_run", r.ID, r.Root); err != nil {
		m.Log.Error("audit compliance run", "error", err)
	}
}
func (m *Manager) snapshot(ctx context.Context, c recorder.Client, w work, r *Run, folder string, v *Result) {
	b, _, err := c.Snapshot(ctx, w.cam.ChannelID)
	if err != nil {
		v.Status = "error"
		v.Detail = err.Error()
		return
	}
	im, format, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		v.Status = "error"
		v.Detail = "Invalid snapshot: " + err.Error()
		return
	}
	if DarkPlaceholder(im) {
		v.Status = "skipped"
		v.Detail = "Dark / Hikvision-style placeholder (image heuristic; low-light scenes may also be skipped)"
		return
	}
	ext := ".jpg"
	if format == "png" {
		ext = ".png"
	}
	dir := filepath.Join(r.Root, folder)
	path := filepath.Join(dir, fmt.Sprintf("%d-%s-%s%s", w.nvr.ID, safepath.Component(w.cam.ChannelID, "channel"), safepath.Component(w.cam.Name, "camera"), ext))
	if err = os.MkdirAll(dir, 0750); err == nil {
		err = os.WriteFile(path, b, 0640)
	}
	if err != nil {
		v.Status = "error"
		v.Detail = err.Error()
		return
	}
	v.Status = "exported"
	v.Path = path
	v.Detail = "Fresh camera snapshot"
}

// DarkPlaceholder detects near-black analog backgrounds with sparse bright logos.
// It is an image heuristic, not proof that a camera is disconnected.
func DarkPlaceholder(im image.Image) bool {
	bounds := im.Bounds()
	var sum, bright, n int64
	for y := bounds.Min.Y; y < bounds.Max.Y; y += 4 {
		for x := bounds.Min.X; x < bounds.Max.X; x += 4 {
			r, g, b, _ := im.At(x, y).RGBA()
			l := (299*int64(r) + 587*int64(g) + 114*int64(b)) / 1000 / 257
			sum += l
			if l > 50 {
				bright++
			}
			n++
		}
	}
	return n > 0 && sum/n < 20 && bright*100/n < 10
}
func (m *Manager) retention(ctx context.Context, c recorder.Client, w work, r *Run, v *Result) {
	loc, err := time.LoadLocation(w.site.Timezone)
	if err != nil {
		v.Status = "error"
		v.Detail = "Invalid site timezone: " + err.Error()
		return
	}
	// Preserve recorder-local clock fields, matching the playback UI semantics.
	local := r.Created.In(loc).AddDate(0, 0, -28)
	target := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute(), local.Second(), 0, time.UTC)
	v.Target = target.Format("2006-01-02 15:04:05") + " (" + w.site.Timezone + ")"
	recs, err := c.Search(ctx, []string{w.cam.ChannelID}, target.Add(-time.Minute), target.Add(time.Minute))
	if err != nil {
		v.Status = "error"
		v.Detail = err.Error()
		return
	}
	for _, rec := range recs {
		if rec.ChannelID != w.cam.ChannelID {
			continue
		}
		start, e1 := wallTime(rec.Start)
		end, e2 := wallTime(rec.End)
		if e1 != nil || e2 != nil {
			v.Status = "error"
			v.Detail = "Recorder returned invalid recording timestamps"
			return
		}
		if !start.After(target) && end.After(target) && rec.PlaybackURI != "" {
			v.Status = "pass"
			v.Detail = "Playback search confirms a recording spanning the 28-day target"
			return
		}
	}
	v.Status = "fail"
	v.Detail = "No recording spans the 28-day target; investigate schedule, gaps, or retention"
}
func wallTime(s string) (time.Time, error) {
	if len(s) < 19 {
		return time.Time{}, fmt.Errorf("invalid timestamp")
	}
	return time.Parse("2006-01-02T15:04:05", s[:19])
}
func ResolveRoot(cfg config.Config, chosen string) (string, error) {
	chosen = strings.TrimSpace(chosen)
	if chosen == "" {
		chosen = "gdpr"
	}
	if filepath.IsAbs(chosen) {
		if !cfg.AllowPrivateDestinations {
			return "", fmt.Errorf("absolute custom destinations are disabled")
		}
		return filepath.Clean(chosen), nil
	}
	base, err := filepath.Abs(cfg.ExportRoot)
	if err != nil {
		return "", err
	}
	out := filepath.Join(base, chosen)
	rel, err := filepath.Rel(base, out)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("root must remain beneath the export directory")
	}
	return out, nil
}

func (m *Manager) discoverCameras(ctx context.Context, n store.NVR) ([]store.Camera, error) {
	timeout := m.Config.RequestTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	client, err := m.NewClient(n, timeout)
	if err != nil {
		return nil, err
	}
	discoveryCtx, cancel := context.WithTimeout(ctx, timeout*3)
	defer cancel()
	cams, err := client.Channels(discoveryCtx)
	if err != nil {
		return nil, err
	}
	if len(cams) == 0 {
		return nil, fmt.Errorf("recorder returned no camera channels")
	}
	if err := m.Store.ReplaceCameras(discoveryCtx, n.ID, cams); err != nil {
		return nil, fmt.Errorf("save discovered cameras: %w", err)
	}
	m.Log.Info("survey camera discovery completed", "recorder", n.ID, "cameras", len(cams))
	return m.Store.ListCameras(discoveryCtx, n.ID)
}

func targets(todo []work) []Target {
	out := make([]Target, 0, len(todo))
	for _, w := range todo {
		out = append(out, Target{SiteID: w.site.ID, NVRID: w.nvr.ID, Site: w.site.Name, Recorder: w.nvr.Name, Camera: w.cam.Name, Channel: w.cam.ChannelID})
	}
	return out
}
