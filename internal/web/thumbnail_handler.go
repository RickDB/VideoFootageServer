package web

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// jobItemThumbnail serves a small first-frame preview of a completed
// camera's secured output file — the same click-to-enlarge thumbnail UI as
// the live recorder snapshot preview, but sourced from the file that was
// actually secured rather than a live ISAPI call. The extracted frame is
// cached forever next to the output file (it's a first frame of a file that
// never changes once secured), so ffmpeg only ever runs once per camera.
func (a *App) jobItemThumbnail(w http.ResponseWriter, r *http.Request) {
	u, ok := currentUser(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	itemID, e := strconv.ParseInt(r.PathValue("item"), 10, 64)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	item, e := a.store.GetJobItem(r.Context(), itemID)
	if e != nil || item.Status != "completed" || item.OutputPath == "" {
		http.NotFound(w, r)
		return
	}
	allowed, e := a.canAccessJobDownload(r, u, item.JobID)
	if e != nil {
		http.Error(w, e.Error(), http.StatusInternalServerError)
		return
	}
	if !allowed {
		http.Error(w, "not authorized", http.StatusForbidden)
		return
	}
	thumbPath := item.OutputPath + ".thumb.jpg"
	if _, err := os.Stat(thumbPath); err != nil {
		if a.cfg.FFmpegPath == "" {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		tmp := thumbPath + ".tmp"
		cmd := exec.CommandContext(ctx, a.cfg.FFmpegPath, "-hide_banner", "-loglevel", "error", "-y",
			"-i", item.OutputPath, "-frames:v", "1", "-vf", "scale=240:-1", "-q:v", "5", "-f", "mjpeg", tmp)
		if out, runErr := cmd.CombinedOutput(); runErr != nil {
			_ = os.Remove(tmp)
			a.log.Warn("extract secured-footage thumbnail", "item", itemID, "error", runErr, "output", string(out))
			http.NotFound(w, r)
			return
		}
		if err := os.Rename(tmp, thumbPath); err != nil {
			_ = os.Remove(tmp)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	f, e := os.Open(thumbPath)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		http.Error(w, e.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, "thumb.jpg", info.ModTime(), f)
}
