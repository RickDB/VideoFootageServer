package web

import (
	"context"
	"net/http"

	"github.com/RickDB/HikFootageServer/internal/recorder"
)

// cameraSnapshot serves a cached live preview image for one camera. It's
// reachable by both roles — the operational export wizard and the
// Collins-style request form both show a small picture preview per camera —
// and is access-checked per site exactly like the other camera/site
// lookups a request-only user can reach, rather than being gated to
// operational users only.
func (a *App) cameraSnapshot(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	cam, e := a.store.GetCamera(r.Context(), id)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	u, _ := currentUser(r)
	if ok, err := a.store.UserCanAccessSite(r.Context(), u, cam.SiteID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if !ok {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "no access to this camera"})
		return
	}
	data, contentType, err := a.snapshots.Get(r.Context(), id, func(ctx context.Context) ([]byte, string, error) {
		nvr, err := a.store.GetNVR(ctx, cam.NVRID)
		if err != nil {
			return nil, "", err
		}
		client, err := recorder.New(nvr, a.cfg.RequestTimeout)
		if err != nil {
			return nil, "", err
		}
		return client.Snapshot(ctx, cam.ChannelID)
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "preview unavailable: " + err.Error()})
		return
	}
	w.Header().Set("Content-Type", contentType)
	// Browser-side caching mirrors the server-side cache window, so a page
	// revisited within 4 hours doesn't even round-trip to re-validate.
	w.Header().Set("Cache-Control", "private, max-age=14400")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
