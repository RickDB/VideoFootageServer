package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/RickDB/HikFootageServer/internal/auth"
	"github.com/RickDB/HikFootageServer/internal/config"
	"github.com/RickDB/HikFootageServer/internal/hikvision"
	"github.com/RickDB/HikFootageServer/internal/jobs"
	"github.com/RickDB/HikFootageServer/internal/store"
)

//go:embed templates/*.html static/*
var assets embed.FS

type App struct {
	cfg       config.Config
	store     *store.Store
	jobs      *jobs.Manager
	auth      *auth.Manager
	log       *slog.Logger
	templates map[string]*template.Template
}
type pageData struct {
	Title         string
	Sites         []store.Site
	NVRs          []store.NVR
	Cameras       []store.Camera
	Jobs          []store.Job
	Site          store.Site
	NVR           store.NVR
	Job           store.Job
	Capabilities  map[string]any
	User          store.User
	Next          string
	Error, Notice string
}

func New(cfg config.Config, s *store.Store, j *jobs.Manager, authManager *auth.Manager, l *slog.Logger) *http.Server {
	a := &App{cfg: cfg, store: s, jobs: j, auth: authManager, log: l, templates: loadTemplates()}
	mux := http.NewServeMux()
	static, _ := fs.Sub(assets, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	mux.HandleFunc("GET /healthz", a.health)
	mux.HandleFunc("GET /login", a.loginPage)
	mux.HandleFunc("POST /login", a.login)
	mux.HandleFunc("POST /logout", a.logout)
	mux.HandleFunc("GET /account", a.accountPage)
	mux.HandleFunc("POST /account/password", a.changePassword)
	mux.HandleFunc("GET /", a.dashboard)
	mux.HandleFunc("GET /sites/new", a.siteForm)
	mux.HandleFunc("POST /sites", a.createSite)
	mux.HandleFunc("GET /nvrs/new", a.nvrForm)
	mux.HandleFunc("POST /nvrs", a.saveNVR)
	mux.HandleFunc("GET /nvrs/{id}", a.nvrEdit)
	mux.HandleFunc("POST /nvrs/{id}", a.updateNVR)
	mux.HandleFunc("POST /nvrs/{id}/test", a.testNVR)
	mux.HandleFunc("GET /exports/new", a.exportForm)
	mux.HandleFunc("POST /api/search", a.search)
	mux.HandleFunc("POST /exports", a.createExport)
	mux.HandleFunc("GET /jobs/{id}", a.jobPage)
	mux.HandleFunc("GET /api/jobs/{id}", a.jobStatus)
	handler := a.requestLog(a.securityHeaders(a.requireAuthentication(mux)))
	return &http.Server{Addr: cfg.ListenAddr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
}
func loadTemplates() map[string]*template.Template {
	out := map[string]*template.Template{}
	fn := template.FuncMap{"eq": func(a, b int64) bool { return a == b }, "eqs": func(a, b string) bool { return a == b }, "timefmt": func(t time.Time) string { return t.Local().Format("02 Jan 2006 15:04") }}
	for _, name := range []string{"dashboard", "site_form", "nvr_form", "export", "job", "login", "account"} {
		out[name] = template.Must(template.New("base.html").Funcs(fn).ParseFS(assets, "templates/base.html", "templates/"+name+".html"))
	}
	return out
}
func (a *App) render(w http.ResponseWriter, name string, d pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if e := a.templates[name].ExecuteTemplate(w, "base", d); e != nil {
		a.log.Error("render page", "template", name, "error", e)
	}
}
func (a *App) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		a.log.Debug("request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start))
	})
}

type userContextKey struct{}

func (a *App) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || !strings.EqualFold(u.Host, r.Host) {
					http.Error(w, "cross-origin request rejected", http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) requireAuthentication(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" || r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		u, err := a.auth.UserFromRequest(r)
		if err != nil {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "authentication required"})
				return
			}
			nextPath := r.URL.RequestURI()
			if r.Method != http.MethodGet {
				nextPath = "/"
			}
			http.Redirect(w, r, "/login?next="+url.QueryEscape(nextPath), http.StatusSeeOther)
			return
		}
		if u.MustChangePassword && r.URL.Path != "/account" && r.URL.Path != "/account/password" && r.URL.Path != "/logout" {
			http.Redirect(w, r, "/account?notice="+url.QueryEscape("Change the bootstrap administrator password before continuing."), http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userContextKey{}, u)))
	})
}

func currentUser(r *http.Request) (store.User, bool) {
	u, ok := r.Context().Value(userContextKey{}).(store.User)
	return u, ok
}

func (a *App) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("ok\n"))
}
func (a *App) loginPage(w http.ResponseWriter, r *http.Request) {
	if _, err := a.auth.UserFromRequest(r); err == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	a.render(w, "login", pageData{Title: "Sign in", Next: safeNext(r.URL.Query().Get("next"))})
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	next := safeNext(r.FormValue("next"))
	u, err := a.auth.Authenticate(r.Context(), r.FormValue("username"), r.FormValue("password"))
	if err != nil {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusUnauthorized)
		a.render(w, "login", pageData{Title: "Sign in", Next: next, Error: "Invalid username or password."})
		return
	}
	token, expires, err := a.auth.CreateSession(r.Context(), u.ID)
	if err != nil {
		http.Error(w, "Unable to create session", http.StatusInternalServerError)
		return
	}
	a.auth.SetCookie(w, token, expires)
	if u.MustChangePassword {
		next = "/account"
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}
func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	a.auth.Logout(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
func (a *App) accountPage(w http.ResponseWriter, r *http.Request) {
	u, _ := currentUser(r)
	a.render(w, "account", pageData{Title: "Account", User: u, Notice: r.URL.Query().Get("notice")})
}
func (a *App) changePassword(w http.ResponseWriter, r *http.Request) {
	u, _ := currentUser(r)
	_ = r.ParseForm()
	next := r.FormValue("new_password")
	if next != r.FormValue("confirm_password") {
		a.render(w, "account", pageData{Title: "Account", User: u, Error: "New passwords do not match."})
		return
	}
	if err := a.auth.ChangePassword(r.Context(), u, r.FormValue("current_password"), next); err != nil {
		a.render(w, "account", pageData{Title: "Account", User: u, Error: err.Error()})
		return
	}
	token, expires, err := a.auth.CreateSession(r.Context(), u.ID)
	if err != nil {
		http.Error(w, "Unable to create session", http.StatusInternalServerError)
		return
	}
	a.auth.SetCookie(w, token, expires)
	http.Redirect(w, r, "/?notice="+url.QueryEscape("Password changed successfully."), http.StatusSeeOther)
}
func safeNext(v string) string {
	if v == "" || !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") {
		return "/"
	}
	return v
}

func idParam(r *http.Request) (int64, error) { return strconv.ParseInt(r.PathValue("id"), 10, 64) }

func (a *App) dashboard(w http.ResponseWriter, r *http.Request) {
	sites, e := a.store.ListSites(r.Context())
	if e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	nvrs, _ := a.store.ListNVRs(r.Context())
	recent, _ := a.store.RecentJobs(r.Context(), 8)
	a.render(w, "dashboard", pageData{Title: "Overview", Sites: sites, NVRs: nvrs, Jobs: recent, Notice: r.URL.Query().Get("notice")})
}
func (a *App) siteForm(w http.ResponseWriter, r *http.Request) {
	a.render(w, "site_form", pageData{Title: "Add customer site", Site: store.Site{Timezone: "UTC"}})
}
func (a *App) createSite(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	v := store.Site{Customer: strings.TrimSpace(r.FormValue("customer")), Name: strings.TrimSpace(r.FormValue("name")), Address: r.FormValue("address"), Timezone: r.FormValue("timezone"), Destination: r.FormValue("destination")}
	if v.Customer == "" || v.Name == "" {
		a.render(w, "site_form", pageData{Title: "Add customer site", Site: v, Error: "Customer and site name are required."})
		return
	}
	id, e := a.store.CreateSite(r.Context(), v)
	if e != nil {
		a.render(w, "site_form", pageData{Title: "Add customer site", Site: v, Error: e.Error()})
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/nvrs/new?site=%d", id), http.StatusSeeOther)
}
func (a *App) nvrForm(w http.ResponseWriter, r *http.Request) {
	sites, _ := a.store.ListSites(r.Context())
	siteID, _ := strconv.ParseInt(r.URL.Query().Get("site"), 10, 64)
	a.render(w, "nvr_form", pageData{Title: "Add recorder", Sites: sites, NVR: store.NVR{SiteID: siteID, Scheme: "https", Port: 443, VerifyTLS: true}})
}
func (a *App) nvrEdit(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	n, e := a.store.GetNVR(r.Context(), id)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	n.Password = ""
	sites, _ := a.store.ListSites(r.Context())
	caps := map[string]any{}
	_ = json.Unmarshal([]byte(n.CapabilitiesJSON), &caps)
	a.render(w, "nvr_form", pageData{Title: "Recorder settings", Sites: sites, NVR: n, Capabilities: caps, Notice: r.URL.Query().Get("notice")})
}
func formNVR(r *http.Request) store.NVR {
	port, _ := strconv.Atoi(r.FormValue("port"))
	site, _ := strconv.ParseInt(r.FormValue("site_id"), 10, 64)
	return store.NVR{SiteID: site, Name: strings.TrimSpace(r.FormValue("name")), Host: strings.TrimSpace(r.FormValue("host")), Port: port, Scheme: r.FormValue("scheme"), Username: r.FormValue("username"), Password: r.FormValue("password"), VerifyTLS: r.FormValue("verify_tls") == "on"}
}
func (a *App) saveNVR(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	n := formNVR(r)
	if e := validateNVR(n); e != nil {
		sites, _ := a.store.ListSites(r.Context())
		a.render(w, "nvr_form", pageData{Title: "Add recorder", Sites: sites, NVR: n, Error: e.Error()})
		return
	}
	id, e := a.store.SaveNVR(r.Context(), n)
	if e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/nvrs/%d?notice=Recorder+saved.+Run+connection+test+to+discover+it.", id), http.StatusSeeOther)
}
func (a *App) updateNVR(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	old, e := a.store.GetNVR(r.Context(), id)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	_ = r.ParseForm()
	n := formNVR(r)
	n.ID = id
	if n.Password == "" {
		n.Password = old.Password
	}
	if e = validateNVR(n); e != nil {
		sites, _ := a.store.ListSites(r.Context())
		n.Password = ""
		a.render(w, "nvr_form", pageData{Title: "Recorder settings", Sites: sites, NVR: n, Error: e.Error()})
		return
	}
	_, e = a.store.SaveNVR(r.Context(), n)
	if e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/nvrs/%d?notice=Recorder+settings+saved.", id), http.StatusSeeOther)
}
func validateNVR(n store.NVR) error {
	if n.SiteID < 1 || n.Name == "" || n.Host == "" || n.Username == "" || n.Password == "" {
		return fmt.Errorf("site, name, host, username and password are required")
	}
	if n.Port < 1 || n.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if n.Scheme != "http" && n.Scheme != "https" {
		return fmt.Errorf("scheme must be HTTP or HTTPS")
	}
	return nil
}
func (a *App) testNVR(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	n, e := a.store.GetNVR(r.Context(), id)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	client, e := hikvision.New(n, a.cfg.RequestTimeout)
	if e == nil {
		var d hikvision.DeviceInfo
		var cams []store.Camera
		var caps hikvision.Capabilities
		d, cams, caps, e = client.Probe(r.Context())
		if e == nil {
			b, _ := json.Marshal(caps)
			_ = a.store.UpdateDiscovery(r.Context(), id, d.Model, d.Serial, d.Firmware, d.DeviceType, "online", string(b))
			for i := range cams {
				cams[i].NVRID = id
			}
			_ = a.store.ReplaceCameras(r.Context(), id, cams)
		}
	}
	if e != nil {
		_ = a.store.UpdateDiscovery(r.Context(), id, n.Model, n.Serial, n.Firmware, n.DeviceType, "offline", n.CapabilitiesJSON)
		http.Redirect(w, r, fmt.Sprintf("/nvrs/%d?notice=%s", id, urlQuery("Connection failed: "+e.Error())), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/nvrs/%d?notice=%s", id, urlQuery("Connection successful; device and cameras discovered.")), http.StatusSeeOther)
}
func urlQuery(v string) string {
	r := strings.NewReplacer("%", "%25", " ", "+", "?", "%3F", "&", "%26", "#", "%23")
	return r.Replace(v)
}

func (a *App) exportForm(w http.ResponseWriter, r *http.Request) {
	sites, _ := a.store.ListSites(r.Context())
	nvrs, _ := a.store.ListNVRs(r.Context())
	var cams []store.Camera
	for _, n := range nvrs {
		cs, _ := a.store.ListCameras(r.Context(), n.ID)
		cams = append(cams, cs...)
	}
	a.render(w, "export", pageData{Title: "Secure footage", Sites: sites, NVRs: nvrs, Cameras: cams})
}

type searchRequest struct {
	NVRID    int64    `json:"nvr_id"`
	Channels []string `json:"channels"`
	Start    string   `json:"start"`
	End      string   `json:"end"`
}

func (a *App) search(w http.ResponseWriter, r *http.Request) {
	var q searchRequest
	if e := json.NewDecoder(r.Body).Decode(&q); e != nil {
		writeJSON(w, 400, map[string]any{"error": "Invalid request"})
		return
	}
	start, e1 := time.Parse(time.RFC3339, q.Start)
	end, e2 := time.Parse(time.RFC3339, q.End)
	if e1 != nil || e2 != nil || !end.After(start) || len(q.Channels) == 0 {
		writeJSON(w, 400, map[string]any{"error": "Choose cameras and a valid start/end range."})
		return
	}
	n, e := a.store.GetNVR(r.Context(), q.NVRID)
	if e != nil {
		writeJSON(w, 404, map[string]any{"error": "Recorder not found"})
		return
	}
	client, e := hikvision.New(n, a.cfg.RequestTimeout)
	if e == nil {
		var recs []store.Recording
		recs, e = client.Search(r.Context(), q.Channels, start, end)
		if e == nil {
			cams, _ := a.store.ListCameras(r.Context(), q.NVRID)
			names := map[string]string{}
			for _, c := range cams {
				names[c.ChannelID] = c.Name
			}
			for i := range recs {
				recs[i].Name = names[recs[i].ChannelID]
			}
			writeJSON(w, 200, map[string]any{"recordings": recs})
			return
		}
	}
	writeJSON(w, 502, map[string]any{"error": e.Error()})
}
func (a *App) createExport(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	nvrID, _ := strconv.ParseInt(r.FormValue("nvr_id"), 10, 64)
	caseRef := strings.TrimSpace(r.FormValue("case_ref"))
	recordings := r.FormValue("recordings")
	var recs []store.Recording
	if json.Unmarshal([]byte(recordings), &recs) != nil || len(recs) == 0 || caseRef == "" {
		http.Error(w, "Case reference and at least one recording are required", 400)
		return
	}
	id, e := a.store.CreateJob(r.Context(), store.Job{NVRID: nvrID, CaseRef: caseRef, Description: r.FormValue("description"), Destination: r.FormValue("destination"), RecordingsJSON: recordings})
	if e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	a.jobs.Wake()
	http.Redirect(w, r, fmt.Sprintf("/jobs/%d", id), http.StatusSeeOther)
}
func (a *App) jobPage(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	j, e := a.store.GetJob(r.Context(), id)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	a.render(w, "job", pageData{Title: "Export job", Job: j})
}
func (a *App) jobStatus(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r)
	if e != nil {
		writeJSON(w, 404, map[string]any{"error": "not found"})
		return
	}
	j, e := a.store.GetJob(r.Context(), id)
	if e != nil {
		writeJSON(w, 404, map[string]any{"error": "not found"})
		return
	}
	var files []string
	_ = json.Unmarshal([]byte(j.OutputFilesJSON), &files)
	writeJSON(w, 200, map[string]any{"id": j.ID, "status": j.Status, "progress": j.Progress, "error": j.Error, "files": files, "updated_at": j.UpdatedAt})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
