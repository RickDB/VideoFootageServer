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
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/RickDB/HikFootageServer/internal/auth"
	"github.com/RickDB/HikFootageServer/internal/config"
	"github.com/RickDB/HikFootageServer/internal/hikvision"
	"github.com/RickDB/HikFootageServer/internal/jobs"
	"github.com/RickDB/HikFootageServer/internal/recorder"
	"github.com/RickDB/HikFootageServer/internal/safepath"
	"github.com/RickDB/HikFootageServer/internal/snapshot"
	"github.com/RickDB/HikFootageServer/internal/store"
	"github.com/RickDB/HikFootageServer/internal/timezones"
)

// snapshotCacheTTL controls how long a fetched camera preview image is
// served from cache before the next request refetches it from the
// recorder, per the 4-hour freshness window camera previews need.
const snapshotCacheTTL = 4 * time.Hour

//go:embed templates/*.html static/*
var assets embed.FS

type App struct {
	cfg       config.Config
	store     *store.Store
	jobs      *jobs.Manager
	auth      *auth.Manager
	log       *slog.Logger
	templates map[string]*template.Template
	snapshots *snapshot.Cache
}
type pageData struct {
	Title          string
	Sites          []store.Site
	NVRs           []store.NVR
	Cameras        []store.Camera
	Jobs           []store.Job
	JobItems       []store.JobItem
	Retrievals     []store.SiteRetrievalStatus
	Site           store.Site
	NVR            store.NVR
	Job            store.Job
	Capabilities   map[string]any
	User           store.User
	Customer       store.Customer
	Customers      []store.Customer
	Users          []store.User
	EditUser       store.User
	UserSiteIDs    map[int64]bool
	Requests       []store.FootageRequest
	Request        store.FootageRequest
	RequestCameras []string
	RequestJobs    []store.Job
	DownloadJobs   []downloadJobView
	Audit          []store.AuditEntry
	Manufacturers  []string
	Next           string
	Theme          string
	Error, Notice  string
}

func New(cfg config.Config, s *store.Store, j *jobs.Manager, authManager *auth.Manager, l *slog.Logger) *http.Server {
	a := &App{cfg: cfg, store: s, jobs: j, auth: authManager, log: l, templates: loadTemplates(), snapshots: snapshot.New(snapshotCacheTTL)}
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
	mux.HandleFunc("GET /sites/new", a.opOnly(a.siteForm))
	mux.HandleFunc("POST /sites", a.opOnly(a.createSite))
	mux.HandleFunc("GET /sites/{id}", a.opOnly(a.siteEdit))
	mux.HandleFunc("POST /sites/{id}", a.opOnly(a.updateSite))
	mux.HandleFunc("POST /sites/{id}/delete", a.opOnly(a.deleteSite))
	mux.HandleFunc("GET /sites/{id}/retrievals", a.opOnly(a.siteRetrievals))
	mux.HandleFunc("POST /sites/{id}/retry", a.opOnly(a.retrySite))
	mux.HandleFunc("GET /nvrs/new", a.opOnly(a.nvrForm))
	mux.HandleFunc("POST /nvrs", a.opOnly(a.saveNVR))
	mux.HandleFunc("GET /nvrs/{id}", a.opOnly(a.nvrEdit))
	mux.HandleFunc("POST /nvrs/{id}", a.opOnly(a.updateNVR))
	mux.HandleFunc("POST /nvrs/{id}/test", a.opOnly(a.testNVR))
	mux.HandleFunc("GET /exports/new", a.reviewOnly(a.exportForm))
	mux.HandleFunc("POST /api/search", a.reviewOnly(a.search))
	mux.HandleFunc("POST /exports", a.reviewOnly(a.createExport))
	mux.HandleFunc("POST /exports/batch", a.reviewOnly(a.createBatchExport))
	mux.HandleFunc("GET /jobs/{id}", a.reviewOnly(a.jobPage))
	mux.HandleFunc("POST /jobs/{id}/retry", a.reviewOnly(a.retryJob))
	mux.HandleFunc("POST /jobs/{id}/retry/{channel}", a.reviewOnly(a.retryCamera))
	mux.HandleFunc("POST /jobs/{id}/delete", a.opOnly(a.deleteJob))
	mux.HandleFunc("POST /jobs/clear", a.opOnly(a.clearJobs))
	mux.HandleFunc("POST /sites/{id}/retrievals/clear", a.opOnly(a.clearSiteJobs))
	mux.HandleFunc("GET /api/jobs/{id}", a.reviewOnly(a.jobStatus))
	mux.HandleFunc("GET /api/retrieval-status", a.reviewOnly(a.retrievalStatus))
	mux.HandleFunc("GET /api/sites/{id}/retrievals", a.opOnly(a.siteRetrievalStatus))
	mux.HandleFunc("GET /settings", a.opOnly(a.settingsPage))
	mux.HandleFunc("POST /settings/theme", a.opOnly(a.updateTheme))
	mux.HandleFunc("GET /settings/customers", a.opOnly(a.customersPage))
	mux.HandleFunc("POST /settings/customers/{id}", a.opOnly(a.updateCustomerHandler))
	mux.HandleFunc("GET /settings/import/uptime-kuma", a.opOnly(a.kumaImportPage))
	mux.HandleFunc("POST /settings/import/uptime-kuma/create", a.opOnly(a.kumaImportCreate))
	mux.HandleFunc("GET /api/existing-recorders", a.opOnly(a.existingRecorders))
	mux.HandleFunc("GET /api/timezones", a.opOnly(a.timezoneList))
	mux.HandleFunc("GET /users", a.opOnly(a.usersList))
	mux.HandleFunc("GET /users/new", a.opOnly(a.userForm))
	mux.HandleFunc("POST /users", a.opOnly(a.createUserHandler))
	mux.HandleFunc("GET /users/{id}", a.opOnly(a.userEdit))
	mux.HandleFunc("POST /users/{id}", a.opOnly(a.updateUserHandler))
	mux.HandleFunc("GET /audit", a.opOnly(a.auditPage))
	mux.HandleFunc("GET /requests", a.requestsList)
	mux.HandleFunc("GET /requests/new", a.requestForm)
	mux.HandleFunc("POST /requests", a.createRequest)
	mux.HandleFunc("GET /requests/{id}", a.requestDetail)
	mux.HandleFunc("POST /requests/{id}/status", a.reviewOnly(a.updateRequestStatusHandler))
	mux.HandleFunc("POST /requests/{id}/link-job", a.reviewOnly(a.linkRequestJobHandler))
	mux.HandleFunc("POST /requests/{id}/approve-and-secure", a.reviewOnly(a.approveAndSecureHandler))
	mux.HandleFunc("GET /api/sites/{id}/cameras", a.siteCameras)
	mux.HandleFunc("GET /api/cameras/{id}/snapshot", a.cameraSnapshot)
	mux.HandleFunc("GET /downloads", a.downloadsPage)
	mux.HandleFunc("GET /downloads/jobs/{id}/zip", a.downloadJobZip)
	mux.HandleFunc("GET /downloads/jobs/{id}/items/{item}", a.downloadJobItem)
	mux.HandleFunc("GET /api/job-items/{item}/thumbnail", a.jobItemThumbnail)
	handler := a.requestLog(a.securityHeaders(a.requireAuthentication(mux)))
	return &http.Server{Addr: cfg.ListenAddr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
}
func loadTemplates() map[string]*template.Template {
	out := map[string]*template.Template{}
	fn := template.FuncMap{
		"eq": func(a, b int64) bool { return a == b }, "eqs": func(a, b string) bool { return a == b },
		"year":    func() int { return time.Now().Year() },
		"timefmt": func(t time.Time) string { return t.Local().Format("02 Jan 2006 15:04") },
		"addDays": func(t time.Time, days int) string { return t.AddDate(0, 0, days).Local().Format("02 Jan 2006") },
		"json": func(v any) template.JS {
			b, err := json.Marshal(v)
			if err != nil {
				b = []byte("null")
			}
			return template.JS(strings.ReplaceAll(string(b), "</", "<\\/"))
		},
	}
	for _, name := range []string{"dashboard", "site_form", "site_retrievals", "nvr_form", "export", "job", "login", "account", "settings", "kuma_import", "users", "user_form", "audit", "customers", "requests_list", "request_form", "request_detail", "downloads"} {
		out[name] = template.Must(template.New("base.html").Funcs(fn).ParseFS(assets, "templates/base.html", "templates/"+name+".html"))
	}
	return out
}
func (a *App) render(w http.ResponseWriter, r *http.Request, name string, d pageData) {
	if u, ok := currentUser(r); ok {
		d.User = u
		if d.Customer.ID == 0 {
			if c, err := a.store.GetCustomer(r.Context(), u.CustomerID); err == nil {
				d.Customer = c
			}
		}
	}
	if d.Theme == "" {
		d.Theme = a.store.GetSetting(context.Background(), "theme", "kfc")
	}
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
		// img-src includes blob: because camera/job-item preview thumbnails
		// are fetched via JS and rendered from an object URL
		// (URL.createObjectURL), not a plain <img src> to the API endpoint —
		// without it every preview thumbnail silently fails to paint (CSP
		// blocks it) even though the fetch itself succeeds.
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
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

// opOnly gates a handler to the operational ("admin") role. A request-only
// (Collins) user hitting one of these routes is sent back to their own home
// page rather than shown a bare error, since the nav never links here for
// them in the first place — this is defense in depth, not the primary UX.
func (a *App) opOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := currentUser(r)
		if !ok || !u.IsOperational() {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeJSON(w, http.StatusForbidden, map[string]any{"error": "operational access required"})
				return
			}
			http.Redirect(w, r, "/requests", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// reviewOnly gates a handler to anyone who can process footage requests —
// an admin or a "reviewer" — the same population as store.User.CanReviewRequests:
// request review/approval, the approve-and-secure action, the manual
// Secure footage export wizard, and viewing/retrying the jobs those queue.
// Narrower than opOnly (a reviewer never reaches site/recorder/user/settings
// management), but the same defense-in-depth shape.
func (a *App) reviewOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := currentUser(r)
		if !ok || !u.CanReviewRequests() {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeJSON(w, http.StatusForbidden, map[string]any{"error": "request-review access required"})
				return
			}
			http.Redirect(w, r, "/requests", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

func currentUser(r *http.Request) (store.User, bool) {
	u, ok := r.Context().Value(userContextKey{}).(store.User)
	return u, ok
}

func (a *App) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("X-HikFootage-Authentication", "required")
	_, _ = w.Write([]byte("ok auth=required\n"))
}
func (a *App) timezoneList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, timezones.All)
}
func (a *App) loginPage(w http.ResponseWriter, r *http.Request) {
	if _, err := a.auth.UserFromRequest(r); err == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	a.render(w, r, "login", pageData{Title: "Sign in", Next: safeNext(r.URL.Query().Get("next"))})
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	next := safeNext(r.FormValue("next"))
	u, err := a.auth.Authenticate(r.Context(), r.FormValue("username"), r.FormValue("password"))
	if err != nil {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusUnauthorized)
		a.render(w, r, "login", pageData{Title: "Sign in", Next: next, Error: "Invalid username or password."})
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
	a.render(w, r, "account", pageData{Title: "Account", User: u, Notice: r.URL.Query().Get("notice")})
}
func (a *App) changePassword(w http.ResponseWriter, r *http.Request) {
	u, _ := currentUser(r)
	_ = r.ParseForm()
	next := r.FormValue("new_password")
	if next != r.FormValue("confirm_password") {
		a.render(w, r, "account", pageData{Title: "Account", User: u, Error: "New passwords do not match."})
		return
	}
	if err := a.auth.ChangePassword(r.Context(), u, r.FormValue("current_password"), next); err != nil {
		a.render(w, r, "account", pageData{Title: "Account", User: u, Error: err.Error()})
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
	if u, ok := currentUser(r); ok && !u.IsOperational() {
		if u.Role == "downloader" {
			http.Redirect(w, r, "/downloads", http.StatusSeeOther)
		} else {
			http.Redirect(w, r, "/requests", http.StatusSeeOther)
		}
		return
	}
	sites, e := a.store.ListSites(r.Context())
	if e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	nvrs, _ := a.store.ListNVRs(r.Context())
	recent, _ := a.store.RecentJobs(r.Context(), 8)
	retrievals, _ := a.store.RetrievalStatuses(r.Context())
	a.render(w, r, "dashboard", pageData{Title: "Overview", Sites: sites, NVRs: nvrs, Jobs: recent, Retrievals: retrievals, Notice: r.URL.Query().Get("notice")})
}
func (a *App) siteForm(w http.ResponseWriter, r *http.Request) {
	customers, _ := a.store.ListCustomers(r.Context())
	a.render(w, r, "site_form", pageData{Title: "Add customer site", Customers: customers, Site: store.Site{Timezone: "UTC", MaxConcurrentDownloads: 1, PollIntervalMinutes: 10, CustomerID: 1}})
}
func formSite(r *http.Request) store.Site {
	limit, _ := strconv.Atoi(r.FormValue("max_concurrent_downloads"))
	poll, _ := strconv.Atoi(r.FormValue("poll_interval_minutes"))
	customerID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	return store.Site{Customer: strings.TrimSpace(r.FormValue("customer")), Name: strings.TrimSpace(r.FormValue("name")), Address: strings.TrimSpace(r.FormValue("address")), Timezone: strings.TrimSpace(r.FormValue("timezone")), Destination: strings.TrimSpace(r.FormValue("destination")), MaxConcurrentDownloads: limit, PollIntervalMinutes: poll, CustomerID: customerID, LocationNumber: strings.TrimSpace(r.FormValue("location_number")), Country: strings.TrimSpace(r.FormValue("country"))}
}
func validateSite(v store.Site) error {
	if v.Customer == "" || v.Name == "" {
		return fmt.Errorf("customer and site name are required")
	}
	if v.Timezone == "" {
		return fmt.Errorf("timezone is required")
	}
	if v.MaxConcurrentDownloads < 1 || v.MaxConcurrentDownloads > 20 {
		return fmt.Errorf("simultaneous downloads must be between 1 and 20")
	}
	if v.PollIntervalMinutes < 5 || v.PollIntervalMinutes > 1440 {
		return fmt.Errorf("recorder status poll interval must be between 5 and 1440 minutes")
	}
	return nil
}
func (a *App) createSite(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	v := formSite(r)
	if err := validateSite(v); err != nil {
		customers, _ := a.store.ListCustomers(r.Context())
		a.render(w, r, "site_form", pageData{Title: "Add customer site", Customers: customers, Site: v, Error: err.Error()})
		return
	}
	id, e := a.store.CreateSite(r.Context(), v)
	if e != nil {
		customers, _ := a.store.ListCustomers(r.Context())
		a.render(w, r, "site_form", pageData{Title: "Add customer site", Customers: customers, Site: v, Error: e.Error()})
		return
	}
	actor, _ := currentUser(r)
	_ = a.store.LogAudit(r.Context(), actor.Username, "site.created", "site", id, "name="+v.Name)
	http.Redirect(w, r, fmt.Sprintf("/nvrs/new?site=%d", id), http.StatusSeeOther)
}
func (a *App) siteEdit(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	site, e := a.store.GetSite(r.Context(), id)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	customers, _ := a.store.ListCustomers(r.Context())
	a.render(w, r, "site_form", pageData{Title: "Edit customer site", Customers: customers, Site: site, Notice: r.URL.Query().Get("notice")})
}
func (a *App) updateSite(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	_ = r.ParseForm()
	site := formSite(r)
	site.ID = id
	if e = validateSite(site); e != nil {
		customers, _ := a.store.ListCustomers(r.Context())
		a.render(w, r, "site_form", pageData{Title: "Edit customer site", Customers: customers, Site: site, Error: e.Error()})
		return
	}
	if e = a.store.UpdateSite(r.Context(), site); e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	actor, _ := currentUser(r)
	_ = a.store.LogAudit(r.Context(), actor.Username, "site.updated", "site", id, "name="+site.Name)
	http.Redirect(w, r, fmt.Sprintf("/sites/%d?notice=%s", id, url.QueryEscape("Site settings saved.")), http.StatusSeeOther)
}
func (a *App) deleteSite(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	if e = a.store.DeleteSite(r.Context(), id); e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	actor, _ := currentUser(r)
	_ = a.store.LogAudit(r.Context(), actor.Username, "site.deleted", "site", id, "")
	http.Redirect(w, r, "/?notice="+url.QueryEscape("Site removed. Its retrieval history remains in the audit database."), http.StatusSeeOther)
}
func (a *App) siteRetrievals(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	site, e := a.store.GetSite(r.Context(), id)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	siteJobs, e := a.store.ListSiteJobs(r.Context(), id, 100)
	if e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	var items []store.JobItem
	for _, job := range siteJobs {
		jobItems, _ := a.store.ListJobItems(r.Context(), job.ID)
		items = append(items, jobItems...)
	}
	a.render(w, r, "site_retrievals", pageData{Title: "Retrieval status", Site: site, Jobs: siteJobs, JobItems: items, Notice: r.URL.Query().Get("notice")})
}
func (a *App) nvrForm(w http.ResponseWriter, r *http.Request) {
	sites, _ := a.store.ListSites(r.Context())
	siteID, _ := strconv.ParseInt(r.URL.Query().Get("site"), 10, 64)
	a.render(w, r, "nvr_form", pageData{Title: "Add recorder", Sites: sites, Manufacturers: recorder.Manufacturers, NVR: store.NVR{SiteID: siteID, Scheme: "https", Port: 443, VerifyTLS: true, Manufacturer: recorder.Hikvision}})
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
	a.render(w, r, "nvr_form", pageData{Title: "Recorder settings", Sites: sites, Manufacturers: recorder.Manufacturers, NVR: n, Capabilities: caps, Notice: r.URL.Query().Get("notice")})
}
func formNVR(r *http.Request) store.NVR {
	port, _ := strconv.Atoi(r.FormValue("port"))
	site, _ := strconv.ParseInt(r.FormValue("site_id"), 10, 64)
	return store.NVR{SiteID: site, Name: strings.TrimSpace(r.FormValue("name")), Host: strings.TrimSpace(r.FormValue("host")), Port: port, Scheme: r.FormValue("scheme"), Username: r.FormValue("username"), Password: r.FormValue("password"), VerifyTLS: r.FormValue("verify_tls") == "on", Manufacturer: r.FormValue("manufacturer")}
}
func (a *App) saveNVR(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	n := formNVR(r)
	if e := validateNVR(n); e != nil {
		sites, _ := a.store.ListSites(r.Context())
		a.render(w, r, "nvr_form", pageData{Title: "Add recorder", Sites: sites, Manufacturers: recorder.Manufacturers, NVR: n, Error: e.Error()})
		return
	}
	id, e := a.store.SaveNVR(r.Context(), n)
	if e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	actor, _ := currentUser(r)
	_ = a.store.LogAudit(r.Context(), actor.Username, "recorder.created", "nvr", id, "name="+n.Name+" host="+n.Host)
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
		a.render(w, r, "nvr_form", pageData{Title: "Recorder settings", Sites: sites, Manufacturers: recorder.Manufacturers, NVR: n, Error: e.Error()})
		return
	}
	actor, _ := currentUser(r)
	_ = a.store.LogAudit(r.Context(), actor.Username, "recorder.updated", "nvr", id, "name="+n.Name)
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
	client, e := recorder.New(n, a.cfg.RequestTimeout)
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
		for i := range cs {
			cs[i].SiteID = n.SiteID
			cs[i].NVRName = n.Name
		}
		cams = append(cams, cs...)
	}
	a.render(w, r, "export", pageData{Title: "Secure footage", Sites: sites, NVRs: nvrs, Cameras: cams})
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
	start, e1 := parseRecorderWallTime(q.Start)
	end, e2 := parseRecorderWallTime(q.End)
	if e1 != nil || e2 != nil || !end.After(start) || len(q.Channels) == 0 {
		writeJSON(w, 400, map[string]any{"error": "Choose cameras and a valid start/end range."})
		return
	}
	n, e := a.store.GetNVR(r.Context(), q.NVRID)
	if e != nil {
		writeJSON(w, 404, map[string]any{"error": "Recorder not found"})
		return
	}
	client, e := recorder.New(n, a.cfg.RequestTimeout)
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

// parseRecorderWallTime deliberately treats a datetime-local value as a clock
// reading on the NVR. The recorder applies its configured timezone and DST
// rules; converting this value in the browser shifts the requested footage.
func parseRecorderWallTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04"} {
		if parsed, err := time.ParseInLocation(layout, value, time.UTC); err == nil {
			return parsed, nil
		}
	}
	return time.Parse(time.RFC3339, value)
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

type batchExportRequest struct {
	CaseRef     string `json:"case_ref"`
	Description string `json:"description"`
	Targets     []struct {
		SiteID     int64             `json:"site_id"`
		NVRID      int64             `json:"nvr_id"`
		Start      string            `json:"start"`
		End        string            `json:"end"`
		Recordings []store.Recording `json:"recordings"`
	} `json:"targets"`
}

func (a *App) createBatchExport(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	var request batchExportRequest
	if err := json.Unmarshal([]byte(r.FormValue("batch")), &request); err != nil || strings.TrimSpace(request.CaseRef) == "" || len(request.Targets) == 0 {
		http.Error(w, "Case reference and at least one site selection are required", http.StatusBadRequest)
		return
	}
	sites := make(map[int64]store.Site)
	for _, target := range request.Targets {
		if len(target.Recordings) == 0 {
			continue
		}
		if err := a.store.ValidateNVRInSite(r.Context(), target.NVRID, target.SiteID); err != nil {
			http.Error(w, "Invalid site/recorder selection: "+err.Error(), http.StatusBadRequest)
			return
		}
		start, startErr := parseRecorderWallTime(target.Start)
		end, endErr := parseRecorderWallTime(target.End)
		if startErr != nil || endErr != nil || !end.After(start) {
			http.Error(w, "Invalid requested recorder time range", http.StatusBadRequest)
			return
		}
		if _, ok := sites[target.SiteID]; !ok {
			site, err := a.store.GetSite(r.Context(), target.SiteID)
			if err != nil {
				http.Error(w, "Site not found", http.StatusBadRequest)
				return
			}
			sites[target.SiteID] = site
		}
	}
	created := make([]int64, 0, len(request.Targets))
	for _, target := range request.Targets {
		if len(target.Recordings) == 0 {
			continue
		}
		recordings, _ := json.Marshal(target.Recordings)
		folder := safepath.Component(fmt.Sprintf("%s - %s to %s", strings.TrimSpace(request.CaseRef), target.Start, target.End), "evidence")
		destination := filepath.Join(strings.TrimSpace(sites[target.SiteID].Destination), folder)
		id, err := a.store.CreateJob(r.Context(), store.Job{NVRID: target.NVRID, CaseRef: strings.TrimSpace(request.CaseRef), Description: request.Description, Destination: destination, DestinationIsFinal: true, RecordingsJSON: string(recordings)})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		created = append(created, id)
	}
	if len(created) == 0 {
		http.Error(w, "No recordings selected", http.StatusBadRequest)
		return
	}
	a.jobs.Wake()
	http.Redirect(w, r, fmt.Sprintf("/jobs/%d?notice=%s", created[0], url.QueryEscape(fmt.Sprintf("Queued %d retrieval job(s).", len(created)))), http.StatusSeeOther)
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
	items, _ := a.store.ListJobItems(r.Context(), id)
	d := pageData{Title: "Export job", Job: j, JobItems: items, Notice: r.URL.Query().Get("notice")}
	if site, err := a.store.GetSite(r.Context(), j.SiteID); err == nil {
		if customer, err := a.store.GetCustomer(r.Context(), site.CustomerID); err == nil {
			d.Customer = customer
		}
	}
	a.render(w, r, "job", d)
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
	items, _ := a.store.ListJobItems(r.Context(), id)
	writeJSON(w, 200, map[string]any{"id": j.ID, "status": j.Status, "progress": j.Progress, "error": j.Error, "files": files, "items": items, "updated_at": j.UpdatedAt})
}

func (a *App) retryJob(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil || a.store.RetryJob(r.Context(), id, "") != nil {
		http.Error(w, "Unable to retry retrieval", http.StatusBadRequest)
		return
	}
	a.jobs.Wake()
	http.Redirect(w, r, fmt.Sprintf("/jobs/%d?notice=%s", id, url.QueryEscape("All cameras queued for retry.")), http.StatusSeeOther)
}

func (a *App) retryCamera(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil || a.store.RetryJob(r.Context(), id, r.PathValue("channel")) != nil {
		http.Error(w, "Unable to retry camera", http.StatusBadRequest)
		return
	}
	a.jobs.Wake()
	http.Redirect(w, r, fmt.Sprintf("/jobs/%d?notice=%s", id, url.QueryEscape("Camera queued for retry.")), http.StatusSeeOther)
}

func (a *App) retrySite(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	count, err := a.store.RetrySite(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.jobs.Wake()
	http.Redirect(w, r, fmt.Sprintf("/sites/%d/retrievals?notice=%s", id, url.QueryEscape(fmt.Sprintf("Queued %d failed retrieval(s) for retry.", count))), http.StatusSeeOther)
}

// auditDeletedJobs writes one append-only entry per job before it's removed
// from the jobs table, so "Recent retrievals" clearing can never erase the
// record that a retrieval happened. This must be called before the store
// delete, never after.
func (a *App) auditDeletedJobs(r *http.Request, jobsToDelete []store.Job) {
	actor, _ := currentUser(r)
	for _, j := range jobsToDelete {
		_ = a.store.LogAudit(r.Context(), actor.Username, "job.deleted", "job", j.ID, fmt.Sprintf("case_ref=%q site=%q status=%s", j.CaseRef, j.SiteName, j.Status))
	}
}

func (a *App) deleteJob(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	_ = r.ParseForm()
	redirect := safeNext(r.FormValue("redirect"))
	if job, jerr := a.store.GetJob(r.Context(), id); jerr == nil {
		a.auditDeletedJobs(r, []store.Job{job})
	}
	if e = a.store.DeleteJob(r.Context(), id); e != nil {
		http.Redirect(w, r, redirect+"?notice="+url.QueryEscape("Unable to delete: "+e.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, redirect+"?notice="+url.QueryEscape("Retrieval removed from history."), http.StatusSeeOther)
}
func (a *App) clearJobs(w http.ResponseWriter, r *http.Request) {
	if toDelete, jerr := a.store.RecentJobs(r.Context(), 100000); jerr == nil {
		a.auditDeletedJobs(r, toDelete)
	}
	n, e := a.store.ClearJobs(r.Context(), 0)
	if e != nil {
		http.Error(w, e.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/?notice="+url.QueryEscape(fmt.Sprintf("Cleared %d retrieval(s) from history.", n)), http.StatusSeeOther)
}
func (a *App) clearSiteJobs(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	if toDelete, jerr := a.store.ListSiteJobs(r.Context(), id, 100000); jerr == nil {
		a.auditDeletedJobs(r, toDelete)
	}
	n, e := a.store.ClearJobs(r.Context(), id)
	if e != nil {
		http.Error(w, e.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/sites/%d/retrievals?notice=%s", id, url.QueryEscape(fmt.Sprintf("Cleared %d retrieval(s) from history.", n))), http.StatusSeeOther)
}

func (a *App) retrievalStatus(w http.ResponseWriter, r *http.Request) {
	statuses, err := a.store.RetrievalStatuses(r.Context())
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"sites": statuses})
}

func (a *App) siteRetrievalStatus(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeJSON(w, 404, map[string]any{"error": "not found"})
		return
	}
	jobsForSite, err := a.store.ListSiteJobs(r.Context(), id, 100)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	items := map[int64][]store.JobItem{}
	for _, job := range jobsForSite {
		items[job.ID], _ = a.store.ListJobItems(r.Context(), job.ID)
	}
	writeJSON(w, 200, map[string]any{"jobs": jobsForSite, "items": items})
}

func (a *App) settingsPage(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "settings", pageData{Title: "Appearance", Theme: a.store.GetSetting(r.Context(), "theme", "kfc"), Notice: r.URL.Query().Get("notice")})
}

func (a *App) updateTheme(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	theme := r.FormValue("theme")
	if theme != "kfc" && theme != "classic" && theme != "charcoal" {
		http.Error(w, "Unknown theme", http.StatusBadRequest)
		return
	}
	if err := a.store.SetSetting(r.Context(), "theme", theme); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings?notice="+url.QueryEscape("Theme applied site-wide."), http.StatusSeeOther)
}
func (a *App) kumaImportPage(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "kuma_import", pageData{Title: "Import from Uptime Kuma", Notice: r.URL.Query().Get("notice")})
}

// existingRecorders is a lightweight lookup the import wizard uses, entirely
// client-side otherwise, to flag monitors that already have a matching
// host/port in the database before offering to create them again.
func (a *App) existingRecorders(w http.ResponseWriter, r *http.Request) {
	nvrs, err := a.store.ListNVRs(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	out := make([]map[string]any, 0, len(nvrs))
	for _, n := range nvrs {
		out = append(out, map[string]any{"host": n.Host, "port": n.Port})
	}
	writeJSON(w, http.StatusOK, out)
}

type kumaImportEntry struct {
	Customer               string `json:"customer"`
	SiteName               string `json:"site_name"`
	Address                string `json:"address"`
	Timezone               string `json:"timezone"`
	Destination            string `json:"destination"`
	MaxConcurrentDownloads int    `json:"max_concurrent_downloads"`
	PollIntervalMinutes    int    `json:"poll_interval_minutes"`
	RecorderName           string `json:"recorder_name"`
	Host                   string `json:"host"`
	Port                   int    `json:"port"`
	Scheme                 string `json:"scheme"`
	Username               string `json:"username"`
	Password               string `json:"password"`
	VerifyTLS              bool   `json:"verify_tls"`
}
type kumaImportRequest struct {
	Entries []kumaImportEntry `json:"entries"`
}
type kumaImportResult struct {
	Index   int    `json:"index"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	SiteID  int64  `json:"site_id,omitempty"`
}

// kumaImportCreate performs the bulk site+recorder creation for step 4 of
// the wizard. The wizard itself parses and maps the Uptime Kuma export
// entirely client-side (the export format varies across Kuma versions and
// never needs to reach the server); this endpoint only ever sees the final,
// already-validated HikFootageServer fields, and re-validates each one
// server-side before creating anything. Each entry is independent so one
// bad row never blocks the rest of the batch.
func (a *App) kumaImportCreate(w http.ResponseWriter, r *http.Request) {
	var req kumaImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Entries) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "No entries to import"})
		return
	}
	if len(req.Entries) > 500 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Too many entries in a single import"})
		return
	}
	results := make([]kumaImportResult, 0, len(req.Entries))
	for i, e := range req.Entries {
		if e.MaxConcurrentDownloads < 1 {
			e.MaxConcurrentDownloads = 1
		}
		if e.PollIntervalMinutes < 5 {
			e.PollIntervalMinutes = 10
		}
		site := store.Site{Customer: strings.TrimSpace(e.Customer), Name: strings.TrimSpace(e.SiteName), Address: strings.TrimSpace(e.Address), Timezone: strings.TrimSpace(e.Timezone), Destination: strings.TrimSpace(e.Destination), MaxConcurrentDownloads: e.MaxConcurrentDownloads, PollIntervalMinutes: e.PollIntervalMinutes}
		if err := validateSite(site); err != nil {
			results = append(results, kumaImportResult{Index: i, Message: err.Error()})
			continue
		}
		nvr := store.NVR{Name: strings.TrimSpace(e.RecorderName), Host: strings.TrimSpace(e.Host), Port: e.Port, Scheme: e.Scheme, Username: e.Username, Password: e.Password, VerifyTLS: e.VerifyTLS}
		if nvr.Name == "" {
			nvr.Name = "NVR"
		}
		// validateNVR also checks SiteID (relevant for the manual "add recorder"
		// form); the real site doesn't exist yet here, so validate a copy with a
		// placeholder ID and assign the real one only after the site is created.
		check := nvr
		check.SiteID = 1
		if err := validateNVR(check); err != nil {
			results = append(results, kumaImportResult{Index: i, Message: err.Error()})
			continue
		}
		if exists, err := a.store.NVRHostPortExists(r.Context(), nvr.Host, nvr.Port); err != nil {
			results = append(results, kumaImportResult{Index: i, Message: "check existing recorders: " + err.Error()})
			continue
		} else if exists {
			results = append(results, kumaImportResult{Index: i, Message: fmt.Sprintf("a recorder at %s:%d already exists", nvr.Host, nvr.Port)})
			continue
		}
		siteID, err := a.store.CreateSite(r.Context(), site)
		if err != nil {
			results = append(results, kumaImportResult{Index: i, Message: "create site: " + err.Error()})
			continue
		}
		nvr.SiteID = siteID
		if _, err := a.store.SaveNVR(r.Context(), nvr); err != nil {
			_ = a.store.HardDeleteSite(r.Context(), siteID)
			results = append(results, kumaImportResult{Index: i, Message: "create recorder: " + err.Error()})
			continue
		}
		results = append(results, kumaImportResult{Index: i, OK: true, Message: "Created", SiteID: siteID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
