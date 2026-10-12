package web

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/RickDB/HikFootageServer/internal/compliance"
)

func (a *App) compliancePage(w http.ResponseWriter, r *http.Request) {
	sites, err := a.store.ListSites(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	runs, err := a.compliance.List(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	d := pageData{Title: "GDPR surveys", Sites: sites, ComplianceRuns: runs, ComplianceRoot: a.store.GetSetting(r.Context(), "compliance_root", "gdpr")}
	if id, err := strconv.ParseInt(r.URL.Query().Get("run"), 10, 64); err == nil {
		run, e := a.compliance.Get(r.Context(), id)
		if e != nil {
			http.NotFound(w, r)
			return
		}
		d.ComplianceRun = run
	}
	a.render(w, r, "compliance", d)
}
func (a *App) complianceRoot(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", 400)
		return
	}
	root := strings.TrimSpace(r.FormValue("root"))
	if _, err := compliance.ResolveRoot(a.cfg, root); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	_, err := a.store.DB.ExecContext(r.Context(), "INSERT INTO settings(key,value) VALUES('compliance_root',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", root)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	u, _ := currentUser(r)
	if err = a.store.LogAudit(r.Context(), u.Username, "compliance.root_changed", "settings", 0, root); err != nil {
		a.log.Error("audit compliance root", "error", err)
	}
	http.Redirect(w, r, "/compliance", http.StatusSeeOther)
}
func (a *App) complianceCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", 400)
		return
	}
	kind := r.FormValue("kind")
	if kind != "snapshots" && kind != "retention" {
		http.Error(w, "Unknown task", 400)
		return
	}
	sites, err := a.store.ListSites(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	allowed := map[int64]bool{}
	for _, s := range sites {
		allowed[s.ID] = true
	}
	ids := []int64{}
	seen := map[int64]bool{}
	if r.FormValue("scope") == "all" {
		for _, s := range sites {
			ids = append(ids, s.ID)
		}
	} else {
		for _, v := range r.Form["site"] {
			id, e := strconv.ParseInt(v, 10, 64)
			if e != nil || !allowed[id] {
				http.Error(w, "Invalid installation", 400)
				return
			}
			if !seen[id] {
				ids = append(ids, id)
				seen[id] = true
			}
		}
	}
	if len(ids) == 0 {
		http.Error(w, "Select at least one installation", 400)
		return
	}
	root, err := compliance.ResolveRoot(a.cfg, a.store.GetSetting(r.Context(), "compliance_root", "gdpr"))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	ref := strings.TrimSpace(r.FormValue("reference"))
	if len(ref) > 120 {
		http.Error(w, "Reference is too long (maximum 120 characters)", 400)
		return
	}
	u, _ := currentUser(r)
	id, err := a.compliance.Create(r.Context(), compliance.Run{Kind: kind, Reference: ref, Actor: u.Username, Root: root, SiteIDs: ids})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/compliance?run=%d", id), http.StatusSeeOther)
}
func (a *App) complianceStatus(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	run, err := a.compliance.Get(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=survey-%d.csv", id))
		out := csv.NewWriter(w)
		_ = out.Write([]string{"Site", "Recorder", "Camera", "Channel", "Status", "Target time", "Details", "Output path", "Checked at"})
		// Neutralize spreadsheet formulas in operator/device-controlled text.
		safe := func(s string) string {
			if strings.ContainsAny(strings.TrimLeft(s, " \t\r\n")[:min(1, len(strings.TrimLeft(s, " \t\r\n")))], "=+-@") {
				return "'" + s
			}
			return s
		}
		for _, v := range run.Results {
			row := []string{v.Site, v.Recorder, v.Camera, v.Channel, v.Status, v.Target, v.Detail, v.Path, v.At.Format("2006-01-02T15:04:05Z07:00")}
			for i := range row {
				row[i] = safe(row[i])
			}
			_ = out.Write(row)
		}
		out.Flush()
		if err := out.Error(); err != nil {
			a.log.Error("write compliance CSV", "error", err)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(run)
}

// complianceRepeat starts a fresh dated run using the same task and selection.
func (a *App) complianceRepeat(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	previous, err := a.compliance.Get(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	root, err := compliance.ResolveRoot(a.cfg, a.store.GetSetting(r.Context(), "compliance_root", "gdpr"))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	u, _ := currentUser(r)
	id, err = a.compliance.Create(r.Context(), compliance.Run{Kind: previous.Kind, Reference: previous.Reference, Actor: u.Username, Root: root, SiteIDs: previous.SiteIDs})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/compliance?run=%d", id), http.StatusSeeOther)
}
