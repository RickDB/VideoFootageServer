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
}
type NVR struct {
	ID, SiteID                                                                                            int64
	Name, Host, Scheme, Username, Password, Model, Serial, Firmware, DeviceType, Status, CapabilitiesJSON string
	Port                                                                                                  int
	VerifyTLS                                                                                             bool
	LastSeen                                                                                              sql.NullTime
}
type Camera struct {
	ID, NVRID       int64
	ChannelID, Name string
	Enabled         bool
}
type Recording struct {
	ChannelID, Name, PlaybackURI, Start, End, ContentType string
	Size                                                  int64
}
type Job struct {
	ID, NVRID                                                                         int64
	CaseRef, Description, Destination, Status, Error, RecordingsJSON, OutputFilesJSON string
	Progress                                                                          int
	CreatedAt, UpdatedAt                                                              time.Time
}
type User struct {
	ID                           int64
	Username, PasswordHash, Role string
	MustChangePassword           bool
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
CREATE TABLE IF NOT EXISTS jobs (id INTEGER PRIMARY KEY, nvr_id INTEGER NOT NULL REFERENCES nvrs(id), case_ref TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', destination TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'queued', progress INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '', recordings_json TEXT NOT NULL, output_files_json TEXT NOT NULL DEFAULT '[]', created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);
CREATE TABLE IF NOT EXISTS users (id INTEGER PRIMARY KEY, username TEXT NOT NULL COLLATE NOCASE UNIQUE, password_hash TEXT NOT NULL, role TEXT NOT NULL DEFAULT 'admin', must_change_password INTEGER NOT NULL DEFAULT 1, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS sessions (token_hash TEXT PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, expires_at DATETIME NOT NULL, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON sessions(expires_at);
`
	_, err := s.DB.Exec(schema)
	return err
}

func (s *Store) GetUserByUsername(ctx context.Context, username string) (User, error) {
	var u User
	err := s.DB.QueryRowContext(ctx, `SELECT id,username,password_hash,role,must_change_password FROM users WHERE username=?`, username).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.MustChangePassword)
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
	err := s.DB.QueryRowContext(ctx, `SELECT u.id,u.username,u.password_hash,u.role,u.must_change_password FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=? AND s.expires_at>?`, tokenHash, now).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.MustChangePassword)
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

func (s *Store) ListSites(ctx context.Context) ([]Site, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT s.id,s.customer,s.name,s.address,s.timezone,s.destination,COUNT(DISTINCT n.id),COUNT(DISTINCT c.id) FROM sites s LEFT JOIN nvrs n ON n.site_id=s.id LEFT JOIN cameras c ON c.nvr_id=n.id GROUP BY s.id ORDER BY s.customer,s.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Site
	for rows.Next() {
		var v Site
		if err := rows.Scan(&v.ID, &v.Customer, &v.Name, &v.Address, &v.Timezone, &v.Destination, &v.NVRCount, &v.CameraCount); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) CreateSite(ctx context.Context, v Site) (int64, error) {
	r, e := s.DB.ExecContext(ctx, `INSERT INTO sites(customer,name,address,timezone,destination) VALUES(?,?,?,?,?)`, v.Customer, v.Name, v.Address, v.Timezone, v.Destination)
	if e != nil {
		return 0, e
	}
	return r.LastInsertId()
}
func (s *Store) GetSite(ctx context.Context, id int64) (Site, error) {
	var v Site
	e := s.DB.QueryRowContext(ctx, `SELECT id,customer,name,address,timezone,destination FROM sites WHERE id=?`, id).Scan(&v.ID, &v.Customer, &v.Name, &v.Address, &v.Timezone, &v.Destination)
	return v, e
}

func (s *Store) ListNVRs(ctx context.Context) ([]NVR, error) {
	return s.listNVRs(ctx, `SELECT id,site_id,name,host,port,scheme,username,password,verify_tls,model,serial,firmware,device_type,status,capabilities_json,last_seen FROM nvrs ORDER BY name`)
}
func (s *Store) ListNVRsBySite(ctx context.Context, id int64) ([]NVR, error) {
	return s.listNVRs(ctx, `SELECT id,site_id,name,host,port,scheme,username,password,verify_tls,model,serial,firmware,device_type,status,capabilities_json,last_seen FROM nvrs WHERE site_id=? ORDER BY name`, id)
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
	r := s.DB.QueryRowContext(ctx, `SELECT id,site_id,name,host,port,scheme,username,password,verify_tls,model,serial,firmware,device_type,status,capabilities_json,last_seen FROM nvrs WHERE id=?`, id)
	e := scanNVR(r, &v, s.codec)
	return v, e
}

type scanner interface{ Scan(...any) error }

func scanNVR(r scanner, v *NVR, c credentialCodec) error {
	var enc string
	if e := r.Scan(&v.ID, &v.SiteID, &v.Name, &v.Host, &v.Port, &v.Scheme, &v.Username, &enc, &v.VerifyTLS, &v.Model, &v.Serial, &v.Firmware, &v.DeviceType, &v.Status, &v.CapabilitiesJSON, &v.LastSeen); e != nil {
		return e
	}
	p, e := c.decrypt(enc)
	v.Password = p
	return e
}
func (s *Store) SaveNVR(ctx context.Context, v NVR) (int64, error) {
	p, e := s.codec.encrypt(v.Password)
	if e != nil {
		return 0, e
	}
	if v.ID == 0 {
		r, e := s.DB.ExecContext(ctx, `INSERT INTO nvrs(site_id,name,host,port,scheme,username,password,verify_tls) VALUES(?,?,?,?,?,?,?,?)`, v.SiteID, v.Name, v.Host, v.Port, v.Scheme, v.Username, p, v.VerifyTLS)
		if e != nil {
			return 0, e
		}
		return r.LastInsertId()
	}
	_, e = s.DB.ExecContext(ctx, `UPDATE nvrs SET site_id=?,name=?,host=?,port=?,scheme=?,username=?,password=?,verify_tls=? WHERE id=?`, v.SiteID, v.Name, v.Host, v.Port, v.Scheme, v.Username, p, v.VerifyTLS, v.ID)
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

func (s *Store) CreateJob(ctx context.Context, j Job) (int64, error) {
	r, e := s.DB.ExecContext(ctx, `INSERT INTO jobs(nvr_id,case_ref,description,destination,recordings_json) VALUES(?,?,?,?,?)`, j.NVRID, j.CaseRef, j.Description, j.Destination, j.RecordingsJSON)
	if e != nil {
		return 0, e
	}
	return r.LastInsertId()
}
func (s *Store) GetJob(ctx context.Context, id int64) (Job, error) {
	var j Job
	e := s.DB.QueryRowContext(ctx, `SELECT id,nvr_id,case_ref,description,destination,status,progress,error,recordings_json,output_files_json,created_at,updated_at FROM jobs WHERE id=?`, id).Scan(&j.ID, &j.NVRID, &j.CaseRef, &j.Description, &j.Destination, &j.Status, &j.Progress, &j.Error, &j.RecordingsJSON, &j.OutputFilesJSON, &j.CreatedAt, &j.UpdatedAt)
	return j, e
}
func (s *Store) NextQueuedJob(ctx context.Context) (Job, error) {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return Job{}, e
	}
	defer tx.Rollback()
	var id int64
	e = tx.QueryRowContext(ctx, `SELECT id FROM jobs WHERE status='queued' ORDER BY id LIMIT 1`).Scan(&id)
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
	_, e := s.DB.ExecContext(ctx, `UPDATE jobs SET status='queued',error='Recovered after application restart',updated_at=CURRENT_TIMESTAMP WHERE status IN ('starting','downloading','verifying')`)
	return e
}
func (s *Store) RecentJobs(ctx context.Context, limit int) ([]Job, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT id,nvr_id,case_ref,description,destination,status,progress,error,recordings_json,output_files_json,created_at,updated_at FROM jobs ORDER BY id DESC LIMIT ?`, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		if e = rows.Scan(&j.ID, &j.NVRID, &j.CaseRef, &j.Description, &j.Destination, &j.Status, &j.Progress, &j.Error, &j.RecordingsJSON, &j.OutputFilesJSON, &j.CreatedAt, &j.UpdatedAt); e != nil {
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
