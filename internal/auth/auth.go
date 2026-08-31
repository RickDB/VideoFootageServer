package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/RickDB/HikFootageServer/internal/config"
	"github.com/RickDB/HikFootageServer/internal/store"
	"golang.org/x/crypto/bcrypt"
)

const CookieName = "hikfootage_session"

var ErrInvalidCredentials = errors.New("invalid username or password")

type Manager struct {
	store         *store.Store
	ttl           time.Duration
	secureCookies bool
}

func New(s *store.Store, cfg config.Config) *Manager {
	return &Manager{store: s, ttl: cfg.SessionTTL, secureCookies: cfg.SecureCookies}
}

func (m *Manager) BootstrapAdmin(ctx context.Context, username, password string) error {
	_, err := m.store.GetUserByUsername(ctx, username)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = m.store.CreateUser(ctx, strings.TrimSpace(username), string(hash), "admin", true)
	return err
}

func (m *Manager) Authenticate(ctx context.Context, username, password string) (store.User, error) {
	u, err := m.store.GetUserByUsername(ctx, strings.TrimSpace(username))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$7EqJtq98hPqEX7fNZaFWoO5u0B.2kQmGqZL7yS2Z2nS3W6z5WQw6K"), []byte(password))
			return store.User{}, ErrInvalidCredentials
		}
		return store.User{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		return store.User{}, ErrInvalidCredentials
	}
	return u, nil
}

func (m *Manager) CreateSession(ctx context.Context, userID int64) (string, time.Time, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	expires := time.Now().UTC().Add(m.ttl)
	if err := m.store.CreateSession(ctx, tokenHash(token), userID, expires); err != nil {
		return "", time.Time{}, err
	}
	_ = m.store.DeleteExpiredSessions(ctx, time.Now().UTC())
	return token, expires, nil
}

func (m *Manager) UserFromRequest(r *http.Request) (store.User, error) {
	cookie, err := r.Cookie(CookieName)
	if err != nil || cookie.Value == "" {
		return store.User{}, ErrInvalidCredentials
	}
	return m.store.UserBySession(r.Context(), tokenHash(cookie.Value), time.Now().UTC())
}

func (m *Manager) SetCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: token, Path: "/", Expires: expires, MaxAge: int(time.Until(expires).Seconds()), HttpOnly: true, Secure: m.secureCookies, SameSite: http.SameSiteStrictMode})
}

func (m *Manager) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(CookieName); err == nil {
		_ = m.store.DeleteSession(r.Context(), tokenHash(cookie.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: m.secureCookies, SameSite: http.SameSiteStrictMode})
}

func (m *Manager) ChangePassword(ctx context.Context, user store.User, current, next string) error {
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(current)) != nil {
		return fmt.Errorf("current password is incorrect")
	}
	if len(next) < 12 {
		return fmt.Errorf("new password must be at least 12 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(next), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err = m.store.UpdateUserPassword(ctx, user.ID, string(hash)); err != nil {
		return err
	}
	return m.store.DeleteUserSessions(ctx, user.ID)
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
