package web

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"github.com/RickDB/HikFootageServer/internal/compliance"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *App) monitoringOverview(w http.ResponseWriter, r *http.Request) {
	o, err := a.compliance.Overview(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(o)
}
func (a *App) monitoringScheduleSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid schedule form", 400)
		return
	}
	id := int64(0)
	var err error
	if r.PathValue("id") != "" {
		id, err = strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "Invalid schedule", 400)
			return
		}
	}
	interval, err := strconv.Atoi(r.FormValue("interval"))
	if err != nil {
		http.Error(w, "Invalid interval", 400)
		return
	}
	u, _ := currentUser(r)
	s := compliance.Schedule{ID: id, Name: strings.TrimSpace(r.FormValue("reference")), Kind: r.FormValue("kind"), Mode: r.FormValue("mode"), StartDate: r.FormValue("start_date"), StartTime: r.FormValue("start_time"), Timezone: strings.TrimSpace(r.FormValue("timezone")), Interval: interval, AllSites: r.FormValue("scope") == "all", Active: r.FormValue("active") == "on", Actor: u.Username}
	for _, v := range r.Form["weekday"] {
		n, e := strconv.Atoi(v)
		if e != nil {
			http.Error(w, "Invalid weekday", 400)
			return
		}
		s.Weekdays = append(s.Weekdays, n)
	}
	sites, err := a.store.ListSites(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	allowed := map[int64]bool{}
	for _, site := range sites {
		allowed[site.ID] = true
	}
	seen := map[int64]bool{}
	if !s.AllSites {
		for _, v := range r.Form["site"] {
			n, e := strconv.ParseInt(v, 10, 64)
			if e != nil || !allowed[n] {
				http.Error(w, "Invalid installation", 400)
				return
			}
			if !seen[n] {
				s.SiteIDs = append(s.SiteIDs, n)
				seen[n] = true
			}
		}
	}
	if err = s.Validate(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if _, err = compliance.ResolveRoot(a.cfg, a.store.GetSetting(r.Context(), "compliance_root", "gdpr")); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if _, err = a.compliance.SaveSchedule(r.Context(), s, time.Now().UTC()); err != nil {
		if err == sql.ErrNoRows {
			http.NotFound(w, r)
		} else {
			http.Error(w, err.Error(), 500)
		}
		return
	}
	http.Redirect(w, r, "/monitoring#schedules", http.StatusSeeOther)
}
func (a *App) monitoringScheduleToggle(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", 400)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	u, _ := currentUser(r)
	if err = a.compliance.ToggleSchedule(r.Context(), id, r.FormValue("active") == "true", u.Username, time.Now().UTC()); err != nil {
		if err == sql.ErrNoRows {
			http.NotFound(w, r)
		} else {
			http.Error(w, err.Error(), 500)
		}
		return
	}
	http.Redirect(w, r, "/monitoring#schedules", http.StatusSeeOther)
}
func siteCSV(w http.ResponseWriter, r compliance.Run) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=monitoring-sites-%d.csv", r.ID))
	out := csv.NewWriter(w)
	_ = out.Write([]string{"Site", "Check", "Status", "NVR", "Scope", "Cameras passed", "Cameras failed", "Errors", "Skipped frames", "Pending", "Affected cameras", "Action"})
	safe := func(s string) string {
		v := strings.TrimSpace(s)
		if len(v) > 0 && strings.ContainsRune("=+-@", rune(v[0])) {
			return "'" + s
		}
		return s
	}
	for _, s := range compliance.Summarize(r) {
		for _, n := range s.Recorders {
			affected := []string{}
			for _, v := range n.Results {
				if v.Status == "fail" || v.Status == "error" || v.Status == "skipped" {
					name := v.Camera
					if name == "" {
						name = "Recorder"
					}
					affected = append(affected, name+" "+v.Channel+": "+v.Detail)
				}
			}
			row := []string{s.Name, s.Kind, s.Status, n.Name, n.Scope, strconv.Itoa(n.Passed + n.Exported), strconv.Itoa(n.Failed), strconv.Itoa(n.Errors), strconv.Itoa(n.Skipped), strconv.Itoa(n.Pending), strings.Join(affected, "; "), s.Action}
			for i := range row {
				row[i] = safe(row[i])
			}
			_ = out.Write(row)
		}
	}
	out.Flush()
}
