package main

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const adminSessionCookie = "memdock_admin_session"
const adminSessionLifetime = 12 * time.Hour
const passwordIterations = 310000

type adminSessionContextKey struct{}
type adminSession struct{ tokenHash string }
type adminCredentials struct {
	Password    string `json:"password"`
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

func (a *app) initializeAdmin(ctx context.Context, password string) error {
	if password == "" {
		return nil
	}
	if !validAdminPassword(password) {
		return errors.New("administrator password must be at least 12 characters")
	}
	hash, err := passwordHash(password)
	if err != nil {
		return err
	}
	_, err = a.db.ExecContext(ctx, `INSERT OR IGNORE INTO admin_credentials(id,password_hash) VALUES(1,?)`, hash)
	return err
}

func validAdminPassword(password string) bool { return utf8.RuneCountInString(password) >= 12 }

func passwordHash(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
	if err != nil {
		return "", err
	}
	return "pbkdf2-sha256$" + strconv.Itoa(passwordIterations) + "$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(key), nil
}

func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	rounds, err := strconv.Atoi(parts[1])
	if err != nil || rounds < 100000 || rounds > 1000000 {
		return false
	}
	salt, e1 := base64.RawStdEncoding.DecodeString(parts[2])
	expected, e2 := base64.RawStdEncoding.DecodeString(parts[3])
	if e1 != nil || e2 != nil || len(salt) < 16 || len(expected) != 32 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, rounds, len(expected))
	return err == nil && hmac.Equal(got, expected)
}

func sessionFromRequest(r *http.Request) (adminSession, bool) {
	s, ok := r.Context().Value(adminSessionContextKey{}).(adminSession)
	return s, ok
}

func (a *app) adminSessionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/admin/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		if a.mode != "private" {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(adminSessionCookie)
		if err == nil && cookie.Value != "" {
			digest := tokenDigest(cookie.Value)
			var csrf string
			var expires int64
			err = a.db.QueryRowContext(r.Context(), `SELECT csrf_hash,expires_at FROM admin_sessions WHERE token_hash=?`, digest).Scan(&csrf, &expires)
			if err == nil && expires > time.Now().Unix() {
				s := adminSession{tokenHash: digest}
				r = r.WithContext(context.WithValue(r.Context(), adminSessionContextKey{}, s))
				if isWriteRequest(r) && r.URL.Path != "/api/v1/admin/login" {
					provided := sha256.Sum256([]byte(r.Header.Get("X-MemDock-CSRF")))
					want, _ := hex.DecodeString(csrf)
					if len(want) != len(provided) || !hmac.Equal(provided[:], want) || !sameOrigin(r) {
						writeError(w, http.StatusForbidden, "invalid CSRF token or origin")
						return
					}
				}
			} else if err == nil {
				_, _ = a.db.ExecContext(r.Context(), `DELETE FROM admin_sessions WHERE token_hash=?`, digest)
			}
		}
		if r.URL.Path == "/api/v1/admin/login" && r.Method == http.MethodPost && !sameOrigin(r) {
			writeError(w, http.StatusForbidden, "same-origin request required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	secure := r.TLS != nil
	// Reverse proxy requests already receive transport policy checks in Secure mode. Private installations should use direct TLS.
	scheme := "http"
	if secure {
		scheme = "https"
	}
	return strings.EqualFold(origin, scheme+"://"+r.Host)
}

func (a *app) adminStatus(w http.ResponseWriter, r *http.Request) {
	if a.mode != "private" {
		http.NotFound(w, r)
		return
	}
	var configured int
	if err := a.db.QueryRowContext(r.Context(), `SELECT count(*) FROM admin_credentials WHERE id=1`).Scan(&configured); err != nil {
		writeError(w, 500, "database error")
		return
	}
	_, logged := sessionFromRequest(r)
	response := map[string]any{"configured": configured != 0, "authenticated": logged}
	if logged {
		s, _ := sessionFromRequest(r)
		var csrf string
		if err := a.db.QueryRowContext(r.Context(), `SELECT csrf_token FROM admin_sessions WHERE token_hash=?`, s.tokenHash).Scan(&csrf); err == nil {
			response["csrf_token"] = csrf
		}
	}
	writeJSON(w, 200, response)
}

func readAdminCredentials(r *http.Request) (adminCredentials, bool) {
	var in adminCredentials
	data, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	if err != nil || len(data) > 4096 || json.Unmarshal(data, &in) != nil {
		return in, false
	}
	return in, true
}
func (a *app) adminLogin(w http.ResponseWriter, r *http.Request) {
	if a.mode != "private" {
		http.NotFound(w, r)
		return
	}
	in, ok := readAdminCredentials(r)
	if !ok {
		writeError(w, 400, "invalid request")
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "could not create session")
		return
	}
	defer tx.Rollback()
	// Acquire SQLite's write lock before checking the password. Password changes
	// use the same lock, so validation and session insertion are serialized with rotation.
	if _, err := tx.ExecContext(r.Context(), `UPDATE admin_credentials SET password_hash=password_hash WHERE id=1`); err != nil {
		writeError(w, 500, "could not create session")
		return
	}
	var encoded string
	if err := tx.QueryRowContext(r.Context(), `SELECT password_hash FROM admin_credentials WHERE id=1`).Scan(&encoded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, 503, "administrator password is not initialized; set MEMDOCK_ADMIN_PASSWORD and restart once")
		} else {
			writeError(w, 500, "could not read administrator credential")
		}
		return
	}
	if !verifyPassword(in.Password, encoded) {
		writeError(w, 401, "invalid password")
		return
	}
	token := make([]byte, 32)
	csrf := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		writeError(w, 500, "could not create session")
		return
	}
	if _, err := rand.Read(csrf); err != nil {
		writeError(w, 500, "could not create session")
		return
	}
	tokenValue := base64.RawURLEncoding.EncodeToString(token)
	csrfValue := base64.RawURLEncoding.EncodeToString(csrf)
	expires := time.Now().Add(adminSessionLifetime)
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO admin_sessions(token_hash,csrf_hash,csrf_token,expires_at) VALUES(?,?,?,?)`, tokenDigest(tokenValue), hex.EncodeToString(sha256Sum([]byte(csrfValue))), csrfValue, expires.Unix()); err != nil {
		writeError(w, 500, "could not create session")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "could not create session")
		return
	}
	setAdminCookie(w, r, tokenValue, expires)
	writeJSON(w, 200, map[string]any{"authenticated": true, "csrf_token": csrfValue, "expires_at": expires.Unix()})
}
func sha256Sum(v []byte) []byte          { s := sha256.Sum256(v); return s[:] }
func secureRequest(r *http.Request) bool { return r.TLS != nil }
func setAdminCookie(w http.ResponseWriter, r *http.Request, value string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: adminSessionCookie, Value: value, Path: "/", HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: int(time.Until(expires).Seconds())})
}
func clearAdminCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: adminSessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteStrictMode, Expires: time.Unix(1, 0), MaxAge: -1})
}
func (a *app) adminLogout(w http.ResponseWriter, r *http.Request) {
	if a.mode != "private" {
		http.NotFound(w, r)
		return
	}
	if s, ok := sessionFromRequest(r); ok {
		_, _ = a.db.ExecContext(r.Context(), `DELETE FROM admin_sessions WHERE token_hash=?`, s.tokenHash)
	}
	clearAdminCookie(w, r)
	writeJSON(w, 200, map[string]bool{"authenticated": false})
}
func (a *app) adminChangePassword(w http.ResponseWriter, r *http.Request) {
	if a.mode != "private" {
		http.NotFound(w, r)
		return
	}
	_, ok := sessionFromRequest(r)
	if !ok {
		writeError(w, 401, "administrator login required")
		return
	}
	in, ok := readAdminCredentials(r)
	if !ok || !validAdminPassword(in.NewPassword) {
		writeError(w, 400, "new password must be at least 12 characters")
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	defer tx.Rollback()
	// Lock the credential row before validating oldPassword. Concurrent rotations
	// serialize here; only the first one can replace the credential.
	if _, err = tx.ExecContext(r.Context(), `UPDATE admin_credentials SET password_hash=password_hash WHERE id=1`); err != nil {
		writeError(w, 500, "could not update password")
		return
	}
	var old string
	if err = tx.QueryRowContext(r.Context(), `SELECT password_hash FROM admin_credentials WHERE id=1`).Scan(&old); err != nil || !verifyPassword(in.OldPassword, old) {
		writeError(w, 401, "invalid current password")
		return
	}
	hash, err := passwordHash(in.NewPassword)
	if err != nil {
		writeError(w, 500, "could not update password")
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE admin_credentials SET password_hash=? WHERE id=1`, hash); err == nil {
		_, err = tx.ExecContext(r.Context(), `DELETE FROM admin_sessions`)
	}
	if err != nil {
		writeError(w, 500, "could not update password")
		return
	}
	if err = tx.Commit(); err != nil {
		writeError(w, 500, "could not update password")
		return
	}
	clearAdminCookie(w, r)
	writeJSON(w, 200, map[string]bool{"authenticated": false})
}
