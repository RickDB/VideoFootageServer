package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	DB    *sql.DB
	codec credentialCodec
}

type Site struct {
	ID                                             int64
	Customer, Name, Address, Timezone, Destination string
	NVRCount, CameraCount                          int
	MaxConcurrentDownloads                         int
	PollIntervalMinutes                            int
	CustomerID                                     int64
	CustomerName, CustomerSlug                     string
	LocationNumber, Country                        string
	DeletedAt                                      sql.NullTime
}
type NVR struct {
	ID, SiteID                                                                                                      int64
	Name, Host, Scheme, Username, Password, Model, Serial, Firmware, DeviceType, Status, CapabilitiesJSON, SiteName string
	Port                                                                                                            int
	VerifyTLS                                                                                                       bool
	LastSeen                                                                                                        sql.NullTime
	Manufacturer                                                                                                    string
	RecordingDaysAvailable                                                                                          int
	RecordingCheckedAt                                                                                              sql.NullTime
}
type Camera struct {
	ID, NVRID, SiteID int64
	ChannelID, Name   string
	NVRName           string
	Enabled           bool
}
type Recording struct {
	ChannelID, Name, PlaybackURI, Start, End, ContentType string
	Size                                                  int64
}
type Job struct {
	ID, NVRID, SiteID                                                                 int64
	CaseRef, Description, Destination, Status, Error, RecordingsJSON, OutputFilesJSON string
	NVRName, SiteName, Customer                                                       string
	Progress                                                                          int
	DestinationIsFinal                                                                bool
	CreatedAt, UpdatedAt                                                              time.Time
}
type JobItem struct {
	ID, JobID, BytesDownloaded, TotalBytes                          int64
	ChannelID, CameraName, RecordingJSON, Status, Error, OutputPath string
	Progress                                                        int
	CreatedAt, UpdatedAt                                            time.Time
}
type SiteRetrievalStatus struct {
	SiteID                                                      int64
	Customer, SiteName, CurrentCamera                           string
	ActiveJobs, QueuedJobs, FailedJobs, CompletedJobs, Progress int
	// UpdatedAt is scanned as a plain string, not time.Time: it comes from
	// MAX(j.updated_at), and SQLite's own CURRENT_TIMESTAMP column default
	// (used by CreateJob, never overridden with an explicit Go time.Time) is
	// serialized differently than the driver's own time.Time encoding, which
	// the driver cannot Scan back into a time.Time — that mismatch used to
	// fail this entire query (and silently blank the dashboard's retrieval
	// widget) the moment any job existed.
	UpdatedAt sql.NullString
}

// Role values: "admin" (full access everywhere — every capability below,
// all sites, user management), "reviewer" (processes footage requests:
// review/approve/decline, the one-click approve-and-secure action, the
// manual Secure footage export wizard, and viewing the jobs it queues —
// but no site/recorder/user/settings management), "downloader" (the
// Downloads area only, with access to every completed retrieval's
// files, for staff who hand off footage but don't process requests), and
// "collins" (request-only: submit and view footage requests, restricted
// to their permitted sites, plus — new — downloading footage from jobs
// linked to their own submissions, no other operational/retrieval UI).
// Every other role's capability check below is explicitly widened to
// include "admin" too, so a new capability never needs a separate
// "is this an admin" check bolted on beside it.
type User struct {
	ID                           int64
	Username, PasswordHash, Role string
	MustChangePassword           bool
	CustomerID                   int64
	CustomerName                 string
	AllSites                     bool
	RequestScope                 string // "own", "sites", or "all" — who's footage requests a collins user can see
	Disabled                     bool
}

// IsOperational is full administrative access: sites, recorders, users,
// settings, and every other capability below.
func (u User) IsOperational() bool { return u.Role == "admin" }

// CanReviewRequests gates footage-request review/approval, the
// approve-and-secure action, the manual Secure footage export wizard, and
// viewing/retrying the jobs those queue — an admin already has this via
// IsOperational, a reviewer has it as their whole job.
func (u User) CanReviewRequests() bool { return u.Role == "admin" || u.Role == "reviewer" }

// CanDownloadAll gates the global (every site/customer) view of the
// Downloads area — an admin, a reviewer (who needs to verify/hand off what
// they just secured), or a downloader (whose whole job is this). Anyone
// else only sees jobs linked to footage requests they personally
// submitted, checked separately per-download rather than here.
func (u User) CanDownloadAll() bool {
	return u.Role == "admin" || u.Role == "reviewer" || u.Role == "downloader"
}

func Open(path, key string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{DB: db, codec: newCodec(key)}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS sites (id INTEGER PRIMARY KEY, customer TEXT NOT NULL, name TEXT NOT NULL, address TEXT NOT NULL DEFAULT '', timezone TEXT NOT NULL DEFAULT 'UTC', destination TEXT NOT NULL DEFAULT '', created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS nvrs (id INTEGER PRIMARY KEY, site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE, name TEXT NOT NULL, host TEXT NOT NULL, port INTEGER NOT NULL, scheme TEXT NOT NULL DEFAULT 'https', username TEXT NOT NULL, password TEXT NOT NULL, verify_tls INTEGER NOT NULL DEFAULT 1, model TEXT NOT NULL DEFAULT '', serial TEXT NOT NULL DEFAULT '', firmware TEXT NOT NULL DEFAULT '', device_type TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'untested', capabilities_json TEXT NOT NULL DEFAULT '{}', last_seen DATETIME, UNIQUE(host,port));
CREATE TABLE IF NOT EXISTS cameras (id INTEGER PRIMARY KEY, nvr_id INTEGER NOT NULL REFERENCES nvrs(id) ON DELETE CASCADE, channel_id TEXT NOT NULL, name TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, last_seen DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE(nvr_id,channel_id));
CREATE TABLE IF NOT EXISTS jobs (id INTEGER PRIMARY KEY, nvr_id INTEGER NOT NULL REFERENCES nvrs(id), case_ref TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', destination TEXT NOT NULL, destination_is_final INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL DEFAULT 'queued', progress INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '', recordings_json TEXT NOT NULL, output_files_json TEXT NOT NULL DEFAULT '[]', created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);
CREATE TABLE IF NOT EXISTS users (id INTEGER PRIMARY KEY, username TEXT NOT NULL COLLATE NOCASE UNIQUE, password_hash TEXT NOT NULL, role TEXT NOT NULL DEFAULT 'admin', must_change_password INTEGER NOT NULL DEFAULT 1, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS sessions (token_hash TEXT PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, expires_at DATETIME NOT NULL, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON sessions(expires_at);
CREATE TABLE IF NOT EXISTS job_items (id INTEGER PRIMARY KEY, job_id INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE, channel_id TEXT NOT NULL, camera_name TEXT NOT NULL DEFAULT '', recording_json TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending', progress INTEGER NOT NULL DEFAULT 0, bytes_downloaded INTEGER NOT NULL DEFAULT 0, total_bytes INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '', output_path TEXT NOT NULL DEFAULT '', created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE INDEX IF NOT EXISTS idx_job_items_job ON job_items(job_id);
CREATE INDEX IF NOT EXISTS idx_job_items_status ON job_items(status);
CREATE TABLE IF NOT EXISTS compliance_runs (id INTEGER PRIMARY KEY, data TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'queued');
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS compliance_runs (id INTEGER PRIMARY KEY, data TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'queued');
INSERT OR IGNORE INTO settings(key,value) VALUES('theme','kfc');
CREATE TABLE IF NOT EXISTS customers (id INTEGER PRIMARY KEY, name TEXT NOT NULL, slug TEXT NOT NULL COLLATE NOCASE UNIQUE, country TEXT NOT NULL DEFAULT '', theme_primary TEXT NOT NULL DEFAULT '', theme_secondary TEXT NOT NULL DEFAULT '', logo_text TEXT NOT NULL DEFAULT '', secured_retention_days INTEGER NOT NULL DEFAULT 32, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
INSERT OR IGNORE INTO customers(id,name,slug,country) VALUES(1,'Internal Operations','internal','');
CREATE TABLE IF NOT EXISTS user_site_access (user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE, PRIMARY KEY(user_id,site_id));
CREATE TABLE IF NOT EXISTS footage_requests (id INTEGER PRIMARY KEY, customer_id INTEGER NOT NULL REFERENCES customers(id), site_id INTEGER NOT NULL REFERENCES sites(id), requested_by INTEGER NOT NULL REFERENCES users(id), requester_name TEXT NOT NULL DEFAULT '', cameras_json TEXT NOT NULL DEFAULT '[]', incident_date TEXT NOT NULL DEFAULT '', start_time TEXT NOT NULL DEFAULT '', end_time TEXT NOT NULL DEFAULT '', reference TEXT NOT NULL DEFAULT '', category TEXT NOT NULL DEFAULT '', comments TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'submitted', job_id INTEGER, reviewed_by TEXT NOT NULL DEFAULT '', reviewer_notes TEXT NOT NULL DEFAULT '', created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE INDEX IF NOT EXISTS idx_requests_status ON footage_requests(status);
CREATE INDEX IF NOT EXISTS idx_requests_site ON footage_requests(site_id);
CREATE TABLE IF NOT EXISTS request_jobs (request_id INTEGER NOT NULL REFERENCES footage_requests(id) ON DELETE CASCADE, job_id INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY(request_id,job_id));
CREATE INDEX IF NOT EXISTS idx_request_jobs_job ON request_jobs(job_id);
CREATE TABLE IF NOT EXISTS audit_log (id INTEGER PRIMARY KEY, at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, actor TEXT NOT NULL DEFAULT '', action TEXT NOT NULL, entity_type TEXT NOT NULL DEFAULT '', entity_id INTEGER NOT NULL DEFAULT 0, details TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS idx_audit_at ON audit_log(at);
`
	if _, err := s.DB.Exec(schema); err != nil {
		return err
	}
	if err := s.ensureColumn("sites", "max_concurrent_downloads", `ALTER TABLE sites ADD COLUMN max_concurrent_downloads INTEGER NOT NULL DEFAULT 1`); err != nil {
		return err
	}
	if err := s.ensureColumn("sites", "deleted_at", `ALTER TABLE sites ADD COLUMN deleted_at DATETIME`); err != nil {
		return err
	}
	if err := s.ensureColumn("jobs", "destination_is_final", `ALTER TABLE jobs ADD COLUMN destination_is_final INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := s.ensureColumn("sites", "poll_interval_minutes", `ALTER TABLE sites ADD COLUMN poll_interval_minutes INTEGER NOT NULL DEFAULT 10`); err != nil {
		return err
	}
	if err := s.ensureColumn("sites", "customer_id", `ALTER TABLE sites ADD COLUMN customer_id INTEGER NOT NULL DEFAULT 1`); err != nil {
		return err
	}
	if err := s.ensureColumn("sites", "location_number", `ALTER TABLE sites ADD COLUMN location_number TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn("sites", "country", `ALTER TABLE sites ADD COLUMN country TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn("users", "customer_id", `ALTER TABLE users ADD COLUMN customer_id INTEGER NOT NULL DEFAULT 1`); err != nil {
		return err
	}
	if err := s.ensureColumn("users", "all_sites", `ALTER TABLE users ADD COLUMN all_sites INTEGER NOT NULL DEFAULT 1`); err != nil {
		return err
	}
	if err := s.ensureColumn("users", "request_scope", `ALTER TABLE users ADD COLUMN request_scope TEXT NOT NULL DEFAULT 'own'`); err != nil {
		return err
	}
	if err := s.ensureColumn("users", "disabled", `ALTER TABLE users ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := s.ensureColumn("nvrs", "manufacturer", `ALTER TABLE nvrs ADD COLUMN manufacturer TEXT NOT NULL DEFAULT 'Hikvision'`); err != nil {
		return err
	}
	if err := s.ensureColumn("nvrs", "recording_days_available", `ALTER TABLE nvrs ADD COLUMN recording_days_available INTEGER NOT NULL DEFAULT -1`); err != nil {
		return err
	}
	if err := s.ensureColumn("nvrs", "recording_checked_at", `ALTER TABLE nvrs ADD COLUMN recording_checked_at DATETIME`); err != nil {
		return err
	}
	// slots_json holds the full list of date/time ranges a footage request
	// covers, as a JSON array — incident_date/start_time/end_time stay in
	// sync as the first slot (for search/sort/display on rows that don't
	// need the rest) so old code and old rows keep working unchanged; a row
	// written before this column existed reads back as empty and falls back
	// to a single slot built from those three columns.
	if err := s.ensureColumn("footage_requests", "slots_json", `ALTER TABLE footage_requests ADD COLUMN slots_json TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	// Collins is the first tenant of the multi-customer request/retrieval
	// model. Seeded once so it's available in the customer picker from the
	// start; an operator fills in real sites/branding via the UI afterwards.
	if _, err := s.DB.Exec(`INSERT OR IGNORE INTO customers(id,name,slug,country,secured_retention_days) VALUES(2,'Collins','collins','Netherlands',32)`); err != nil {
		return err
	}
	return nil
}

func (s *Store) ensureColumn(table, column, ddl string) error {
	rows, err := s.DB.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var def any
		if err = rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	_, err = s.DB.Exec(ddl)
	return err
}

const userColumns = `u.id,u.username,u.password_hash,u.role,u.must_change_password,u.customer_id,u.all_sites,u.request_scope,u.disabled,cu.name`

func scanUser(r scanner, u *User) error {
	return r.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.MustChangePassword, &u.CustomerID, &u.AllSites, &u.RequestScope, &u.Disabled, &u.CustomerName)
}

func (s *Store) GetUserByUsername(ctx context.Context, username string) (User, error) {
	var u User
	err := scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users u JOIN customers cu ON cu.id=u.customer_id WHERE u.username=?`, username), &u)
	return u, err
}
func (s *Store) CreateUser(ctx context.Context, username, passwordHash, role string, mustChange bool) (User, error) {
	r, err := s.DB.ExecContext(ctx, `INSERT INTO users(username,password_hash,role,must_change_password) VALUES(?,?,?,?)`, username, passwordHash, role, mustChange)
	if err != nil {
		return User{}, err
	}
	id, err := r.LastInsertId()
	if err != nil {
		return User{}, err
	}
	return User{ID: id, Username: username, PasswordHash: passwordHash, Role: role, MustChangePassword: mustChange}, nil
}
func (s *Store) UpdateUserPassword(ctx context.Context, userID int64, passwordHash string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE users SET password_hash=?,must_change_password=0,updated_at=CURRENT_TIMESTAMP WHERE id=?`, passwordHash, userID)
	return err
}
func (s *Store) CreateSession(ctx context.Context, tokenHash string, userID int64, expires time.Time) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES(?,?,?)`, tokenHash, userID, expires)
	return err
}
func (s *Store) UserBySession(ctx context.Context, tokenHash string, now time.Time) (User, error) {
	var u User
	err := scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userColumns+` FROM sessions s JOIN users u ON u.id=s.user_id JOIN customers cu ON cu.id=u.customer_id WHERE s.token_hash=? AND s.expires_at>?`, tokenHash, now), &u)
	if err == nil && u.Disabled {
		return User{}, sql.ErrNoRows
	}
	return u, err
}
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=?`, tokenHash)
	return err
}
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, userID)
	return err
}
func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at<=?`, now)
	return err
}

const siteColumns = `s.id,s.customer,s.name,s.address,s.timezone,s.destination,s.max_concurrent_downloads,s.poll_interval_minutes,s.customer_id,s.location_number,s.country,cu.name,cu.slug`

func (s *Store) ListSites(ctx context.Context) ([]Site, error) {
	return s.listSites(ctx, `SELECT `+siteColumns+`,COUNT(DISTINCT n.id),COUNT(DISTINCT c.id) FROM sites s JOIN customers cu ON cu.id=s.customer_id LEFT JOIN nvrs n ON n.site_id=s.id LEFT JOIN cameras c ON c.nvr_id=n.id WHERE s.deleted_at IS NULL GROUP BY s.id ORDER BY s.customer,s.name`)
}

// ListSitesForCustomer scopes the site list to one tenant, used by the
// Collins-facing request form so a customer only ever sees their own sites.
func (s *Store) ListSitesForCustomer(ctx context.Context, customerID int64) ([]Site, error) {
	return s.listSites(ctx, `SELECT `+siteColumns+`,COUNT(DISTINCT n.id),COUNT(DISTINCT c.id) FROM sites s JOIN customers cu ON cu.id=s.customer_id LEFT JOIN nvrs n ON n.site_id=s.id LEFT JOIN cameras c ON c.nvr_id=n.id WHERE s.deleted_at IS NULL AND s.customer_id=? GROUP BY s.id ORDER BY s.name`, customerID)
}

func (s *Store) listSites(ctx context.Context, q string, args ...any) ([]Site, error) {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Site
	for rows.Next() {
		var v Site
		if err := rows.Scan(&v.ID, &v.Customer, &v.Name, &v.Address, &v.Timezone, &v.Destination, &v.MaxConcurrentDownloads, &v.PollIntervalMinutes, &v.CustomerID, &v.LocationNumber, &v.Country, &v.CustomerName, &v.CustomerSlug, &v.NVRCount, &v.CameraCount); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) CreateSite(ctx context.Context, v Site) (int64, error) {
	if v.MaxConcurrentDownloads < 1 {
		v.MaxConcurrentDownloads = 1
	}
	if v.PollIntervalMinutes < 5 {
		v.PollIntervalMinutes = 10
	}
	if v.CustomerID < 1 {
		v.CustomerID = 1
	}
	r, e := s.DB.ExecContext(ctx, `INSERT INTO sites(customer,name,address,timezone,destination,max_concurrent_downloads,poll_interval_minutes,customer_id,location_number,country) VALUES(?,?,?,?,?,?,?,?,?,?)`, v.Customer, v.Name, v.Address, v.Timezone, v.Destination, v.MaxConcurrentDownloads, v.PollIntervalMinutes, v.CustomerID, v.LocationNumber, v.Country)
	if e != nil {
		return 0, e
	}
	return r.LastInsertId()
}
func (s *Store) GetSite(ctx context.Context, id int64) (Site, error) {
	var v Site
	e := s.DB.QueryRowContext(ctx, `SELECT `+siteColumns+`,s.deleted_at FROM sites s JOIN customers cu ON cu.id=s.customer_id WHERE s.id=? AND s.deleted_at IS NULL`, id).Scan(&v.ID, &v.Customer, &v.Name, &v.Address, &v.Timezone, &v.Destination, &v.MaxConcurrentDownloads, &v.PollIntervalMinutes, &v.CustomerID, &v.LocationNumber, &v.Country, &v.CustomerName, &v.CustomerSlug, &v.DeletedAt)
	return v, e
}

func (s *Store) UpdateSite(ctx context.Context, v Site) error {
	if v.CustomerID < 1 {
		v.CustomerID = 1
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE sites SET customer=?,name=?,address=?,timezone=?,destination=?,max_concurrent_downloads=?,poll_interval_minutes=?,customer_id=?,location_number=?,country=? WHERE id=? AND deleted_at IS NULL`, v.Customer, v.Name, v.Address, v.Timezone, v.Destination, v.MaxConcurrentDownloads, v.PollIntervalMinutes, v.CustomerID, v.LocationNumber, v.Country, v.ID)
	return err
}

// SitesForPolling returns every non-deleted site's poll interval, used by
// the background recorder-status poller to pace itself per site.
func (s *Store) SitesForPolling(ctx context.Context) ([]Site, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,poll_interval_minutes FROM sites WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Site
	for rows.Next() {
		var v Site
		if err := rows.Scan(&v.ID, &v.PollIntervalMinutes); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) DeleteSite(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE sites SET deleted_at=CURRENT_TIMESTAMP WHERE id=? AND deleted_at IS NULL`, id)
	return err
}

// HardDeleteSite permanently removes a site with no recorders yet attached.
// Used only to roll back a site created moments earlier by the bulk import
// wizard when its paired recorder then fails to save (e.g. a host/port
// collision), so the import never leaves an orphan site behind.
func (s *Store) HardDeleteSite(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sites WHERE id=? AND NOT EXISTS(SELECT 1 FROM nvrs WHERE nvrs.site_id=sites.id)`, id)
	return err
}

// NVRHostPortExists reports whether a recorder with this host/port is
// already configured, used by the Uptime Kuma import wizard to skip
// duplicates before creating a new site for them.
func (s *Store) NVRHostPortExists(ctx context.Context, host string, port int) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM nvrs WHERE host=? AND port=?`, host, port).Scan(&n)
	return n > 0, err
}

const nvrColumns = `n.id,n.site_id,n.name,n.host,n.port,n.scheme,n.username,n.password,n.verify_tls,n.model,n.serial,n.firmware,n.device_type,n.status,n.capabilities_json,n.last_seen,s.name,n.manufacturer,n.recording_days_available,n.recording_checked_at`

func (s *Store) ListNVRs(ctx context.Context) ([]NVR, error) {
	return s.listNVRs(ctx, `SELECT `+nvrColumns+` FROM nvrs n JOIN sites s ON s.id=n.site_id WHERE s.deleted_at IS NULL ORDER BY n.name`)
}
func (s *Store) ListNVRsBySite(ctx context.Context, id int64) ([]NVR, error) {
	return s.listNVRs(ctx, `SELECT `+nvrColumns+` FROM nvrs n JOIN sites s ON s.id=n.site_id WHERE n.site_id=? AND s.deleted_at IS NULL ORDER BY n.name`, id)
}
func (s *Store) listNVRs(ctx context.Context, q string, args ...any) ([]NVR, error) {
	rows, e := s.DB.QueryContext(ctx, q, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []NVR
	for rows.Next() {
		var v NVR
		if e = scanNVR(rows, &v, s.codec); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) GetNVR(ctx context.Context, id int64) (NVR, error) {
	var v NVR
	r := s.DB.QueryRowContext(ctx, `SELECT `+nvrColumns+` FROM nvrs n JOIN sites s ON s.id=n.site_id WHERE n.id=?`, id)
	e := scanNVR(r, &v, s.codec)
	return v, e
}

type scanner interface{ Scan(...any) error }

func scanNVR(r scanner, v *NVR, c credentialCodec) error {
	var enc string
	if e := r.Scan(&v.ID, &v.SiteID, &v.Name, &v.Host, &v.Port, &v.Scheme, &v.Username, &enc, &v.VerifyTLS, &v.Model, &v.Serial, &v.Firmware, &v.DeviceType, &v.Status, &v.CapabilitiesJSON, &v.LastSeen, &v.SiteName, &v.Manufacturer, &v.RecordingDaysAvailable, &v.RecordingCheckedAt); e != nil {
		return e
	}
	p, e := c.decrypt(enc)
	v.Password = p
	return e
}

// RecordingAvailability buckets an NVR's last recording-availability check
// against the 28-day minimum retrievable-footage window. This is distinct
// from a customer's secured-footage retention setting: availability is what
// the recorder itself still has on disk; retention is how long a completed
// retrieval job's output is kept before it would be safe to purge.
func (v NVR) RecordingAvailability() string {
	if v.Status == "offline" {
		return "Offline"
	}
	if !v.RecordingCheckedAt.Valid || v.RecordingDaysAvailable < 0 {
		return "Unknown"
	}
	switch {
	case v.RecordingDaysAvailable >= 28:
		return "OK"
	case v.RecordingDaysAvailable >= 14:
		return "Warning"
	default:
		return "Error"
	}
}

// UpdateRecordingAvailability records the outcome of the background 28-day
// recording-availability check, independent of the online/offline status
// touched by UpdateNVRStatus.
func (s *Store) UpdateRecordingAvailability(ctx context.Context, id int64, days int) error {
	_, e := s.DB.ExecContext(ctx, `UPDATE nvrs SET recording_days_available=?,recording_checked_at=CURRENT_TIMESTAMP WHERE id=?`, days, id)
	return e
}

// UpdateNVRStatus is a lightweight status/last-seen touch used by the
// background poller. Unlike UpdateDiscovery it never overwrites the
// previously discovered model/serial/firmware/capabilities, so a routine
// availability ping can't clobber data only a full "Test & discover" run
// should change.
func (s *Store) UpdateNVRStatus(ctx context.Context, id int64, status string) error {
	_, e := s.DB.ExecContext(ctx, `UPDATE nvrs SET status=?,last_seen=CURRENT_TIMESTAMP WHERE id=?`, status, id)
	return e
}
func (s *Store) SaveNVR(ctx context.Context, v NVR) (int64, error) {
	p, e := s.codec.encrypt(v.Password)
	if e != nil {
		return 0, e
	}
	if v.Manufacturer == "" {
		v.Manufacturer = "Hikvision"
	}
	if v.ID == 0 {
		r, e := s.DB.ExecContext(ctx, `INSERT INTO nvrs(site_id,name,host,port,scheme,username,password,verify_tls,manufacturer) VALUES(?,?,?,?,?,?,?,?,?)`, v.SiteID, v.Name, v.Host, v.Port, v.Scheme, v.Username, p, v.VerifyTLS, v.Manufacturer)
		if e != nil {
			return 0, e
		}
		return r.LastInsertId()
	}
	_, e = s.DB.ExecContext(ctx, `UPDATE nvrs SET site_id=?,name=?,host=?,port=?,scheme=?,username=?,password=?,verify_tls=?,manufacturer=? WHERE id=?`, v.SiteID, v.Name, v.Host, v.Port, v.Scheme, v.Username, p, v.VerifyTLS, v.Manufacturer, v.ID)
	return v.ID, e
}
func (s *Store) UpdateDiscovery(ctx context.Context, id int64, model, serial, firmware, deviceType, status, caps string) error {
	_, e := s.DB.ExecContext(ctx, `UPDATE nvrs SET model=?,serial=?,firmware=?,device_type=?,status=?,capabilities_json=?,last_seen=CURRENT_TIMESTAMP WHERE id=?`, model, serial, firmware, deviceType, status, caps, id)
	return e
}
func (s *Store) ReplaceCameras(ctx context.Context, nvrID int64, cams []Camera) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for _, c := range cams {
		_, e = tx.ExecContext(ctx, `INSERT INTO cameras(nvr_id,channel_id,name,enabled,last_seen) VALUES(?,?,?,?,CURRENT_TIMESTAMP) ON CONFLICT(nvr_id,channel_id) DO UPDATE SET name=excluded.name,enabled=excluded.enabled,last_seen=CURRENT_TIMESTAMP`, nvrID, c.ChannelID, c.Name, c.Enabled)
		if e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (s *Store) ListCameras(ctx context.Context, nvrID int64) ([]Camera, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT id,nvr_id,channel_id,name,enabled FROM cameras WHERE nvr_id=? ORDER BY CAST(channel_id AS INTEGER),channel_id`, nvrID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Camera
	for rows.Next() {
		var c Camera
		if e = rows.Scan(&c.ID, &c.NVRID, &c.ChannelID, &c.Name, &c.Enabled); e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetCamera looks up a single camera along with the site it belongs to
// (via its recorder), used by the snapshot endpoint to run the same
// site-scoped access check as everything else a request-only user can
// reach, without exposing the full recorder/site management surface.
func (s *Store) GetCamera(ctx context.Context, id int64) (Camera, error) {
	var c Camera
	e := s.DB.QueryRowContext(ctx, `SELECT c.id,c.nvr_id,n.site_id,c.channel_id,c.name,c.enabled,n.name FROM cameras c JOIN nvrs n ON n.id=c.nvr_id WHERE c.id=?`, id).
		Scan(&c.ID, &c.NVRID, &c.SiteID, &c.ChannelID, &c.Name, &c.Enabled, &c.NVRName)
	return c, e
}

func (s *Store) CreateJob(ctx context.Context, j Job) (int64, error) {
	var recordings []Recording
	if err := json.Unmarshal([]byte(j.RecordingsJSON), &recordings); err != nil || len(recordings) == 0 {
		return 0, fmt.Errorf("job requires at least one recording")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, `INSERT INTO jobs(nvr_id,case_ref,description,destination,destination_is_final,recordings_json) VALUES(?,?,?,?,?,?)`, j.NVRID, j.CaseRef, j.Description, j.Destination, j.DestinationIsFinal, j.RecordingsJSON)
	if err != nil {
		return 0, err
	}
	id, err := r.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, recording := range recordings {
		b, _ := json.Marshal(recording)
		if _, err = tx.ExecContext(ctx, `INSERT INTO job_items(job_id,channel_id,camera_name,recording_json,total_bytes) VALUES(?,?,?,?,?)`, id, recording.ChannelID, recording.Name, string(b), recording.Size); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

// jobColumns is shared by every query that hydrates a full Job (GetJob and
// ListJobsForRequest today) so their column list and scanJob can never
// silently drift apart.
const jobColumns = `j.id,j.nvr_id,n.site_id,j.case_ref,j.description,j.destination,j.destination_is_final,j.status,j.progress,j.error,j.recordings_json,j.output_files_json,j.created_at,j.updated_at,n.name,s.name,s.customer`

func (s *Store) GetJob(ctx context.Context, id int64) (Job, error) {
	var j Job
	e := scanJob(s.DB.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs j JOIN nvrs n ON n.id=j.nvr_id JOIN sites s ON s.id=n.site_id WHERE j.id=?`, id), &j)
	return j, e
}
func scanJob(row scanner, j *Job) error {
	return row.Scan(&j.ID, &j.NVRID, &j.SiteID, &j.CaseRef, &j.Description, &j.Destination, &j.DestinationIsFinal, &j.Status, &j.Progress, &j.Error, &j.RecordingsJSON, &j.OutputFilesJSON, &j.CreatedAt, &j.UpdatedAt, &j.NVRName, &j.SiteName, &j.Customer)
}
func (s *Store) NextQueuedJob(ctx context.Context) (Job, error) {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return Job{}, e
	}
	defer tx.Rollback()
	var id int64
	e = tx.QueryRowContext(ctx, `SELECT j.id FROM jobs j JOIN nvrs n ON n.id=j.nvr_id JOIN sites s ON s.id=n.site_id WHERE j.status='queued' AND s.deleted_at IS NULL AND (SELECT COUNT(*) FROM jobs active JOIN nvrs active_nvr ON active_nvr.id=active.nvr_id WHERE active_nvr.site_id=n.site_id AND active.status IN ('starting','downloading','verifying')) < s.max_concurrent_downloads ORDER BY j.id LIMIT 1`).Scan(&id)
	if e != nil {
		return Job{}, e
	}
	r, e := tx.ExecContext(ctx, `UPDATE jobs SET status='starting',updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='queued'`, id)
	if e != nil {
		return Job{}, e
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return Job{}, sql.ErrNoRows
	}
	if e = tx.Commit(); e != nil {
		return Job{}, e
	}
	return s.GetJob(ctx, id)
}
func (s *Store) UpdateJob(ctx context.Context, id int64, status string, progress int, errText string, files []string) error {
	b, _ := json.Marshal(files)
	_, e := s.DB.ExecContext(ctx, `UPDATE jobs SET status=?,progress=?,error=?,output_files_json=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, status, progress, errText, string(b), id)
	return e
}
func (s *Store) RecoverJobs(ctx context.Context) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `UPDATE job_items SET status='pending',progress=0,bytes_downloaded=0,error='Recovered after application restart',updated_at=CURRENT_TIMESTAMP WHERE status IN ('downloading','remuxing')`); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE jobs SET status='queued',error='Recovered after application restart',updated_at=CURRENT_TIMESTAMP WHERE status IN ('starting','downloading','verifying')`); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) RecentJobs(ctx context.Context, limit int) ([]Job, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT j.id,j.nvr_id,n.site_id,j.case_ref,j.description,j.destination,j.destination_is_final,j.status,j.progress,j.error,j.recordings_json,j.output_files_json,j.created_at,j.updated_at,n.name,s.name,s.customer FROM jobs j JOIN nvrs n ON n.id=j.nvr_id JOIN sites s ON s.id=n.site_id ORDER BY j.id DESC LIMIT ?`, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		if e = scanJob(rows, &j); e != nil {
			return nil, e
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

type credentialCodec struct{ aead cipher.AEAD }

func newCodec(key string) credentialCodec {
	if key == "" {
		return credentialCodec{}
	}
	sum := sha256.Sum256([]byte(key))
	b, _ := aes.NewCipher(sum[:])
	a, _ := cipher.NewGCM(b)
	return credentialCodec{aead: a}
}
func (c credentialCodec) encrypt(v string) (string, error) {
	if c.aead == nil {
		return "plain:" + base64.StdEncoding.EncodeToString([]byte(v)), nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, e := io.ReadFull(rand.Reader, nonce); e != nil {
		return "", e
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(v), nil)
	return "gcm:" + base64.StdEncoding.EncodeToString(sealed), nil
}
func (c credentialCodec) decrypt(v string) (string, error) {
	if len(v) > 6 && v[:6] == "plain:" {
		b, e := base64.StdEncoding.DecodeString(v[6:])
		return string(b), e
	}
	if len(v) < 4 || v[:4] != "gcm:" {
		return "", errors.New("unknown credential format")
	}
	if c.aead == nil {
		return "", errors.New("APP_ENCRYPTION_KEY is required to decrypt NVR credentials")
	}
	b, e := base64.StdEncoding.DecodeString(v[4:])
	if e != nil {
		return "", e
	}
	n := c.aead.NonceSize()
	if len(b) < n {
		return "", fmt.Errorf("invalid credential")
	}
	p, e := c.aead.Open(nil, b[:n], b[n:], nil)
	return string(p), e
}
