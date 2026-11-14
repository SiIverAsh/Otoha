package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/SiIverAsh/otoha/internal/api/handlers"
	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/db"
	"golang.org/x/crypto/bcrypt"
)

const SessionCookieName = "otoha_session"

// UserFromContext returns the authenticated user (or default single-user id=1 when auth is off).
func UserFromContext(ctx context.Context) *db.User {
	return handlers.UserFromContext(ctx)
}

// HashPassword hashes plaintext password using bcrypt (§9: never invent custom crypto).
func HashPassword(plain string) (string, error) {
	if len(plain) < 4 {
		return "", errors.New("password must be at least 4 characters")
	}
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// VerifyPassword checks plaintext password against stored bcrypt hash.
func VerifyPassword(hash, plain string) bool {
	if hash == "" || plain == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// GenerateSessionToken creates a cryptographically random 256-bit hex session token.
func GenerateSessionToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// AuthMiddleware enforces authentication when cfg.AuthEnabled is true, while seamlessly injecting
// default user (id=1) when cfg.AuthEnabled is false (§9). Supports Session Cookie, Bearer token,
// `?token=` query (for audio/SSE), and HTTP Basic Auth (for WebDAV clients).
func AuthMiddleware(store *db.Store, cfg *config.Config, requireForWebDAV bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snap := cfg.Snapshot()
		if !snap.AuthEnabled {
			u := &db.User{ID: 1, Username: "default", IsAdmin: true}
			next.ServeHTTP(w, r.WithContext(handlers.WithUser(r.Context(), u)))
			return
		}

		user := resolveAuthenticatedUser(r, store)
		if user == nil {
			if requireForWebDAV {
				w.Header().Set("WWW-Authenticate", `Basic realm="Otoha WebDAV"`)
			}
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(handlers.WithUser(r.Context(), user)))
	})
}

func resolveAuthenticatedUser(r *http.Request, store *db.Store) *db.User {
	ctx := r.Context()

	// 1. Cookie
	if c, err := r.Cookie(SessionCookieName); err == nil && c.Value != "" {
		if u, err := store.GetUserBySession(ctx, c.Value); err == nil {
			return u
		}
	}

	// 2. Authorization: Bearer <token>
	authHdr := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(authHdr), "bearer ") {
		tok := strings.TrimSpace(authHdr[7:])
		if u, err := store.GetUserBySession(ctx, tok); err == nil {
			return u
		}
	}

	// 3. Query param `?token=` (useful for <audio src> or EventSource when cookies aren't sent cross-origin)
	if tok := strings.TrimSpace(r.URL.Query().Get("token")); tok != "" {
		if u, err := store.GetUserBySession(ctx, tok); err == nil {
			return u
		}
	}

	// 4. HTTP Basic Auth (essential for WebDAV clients like Infuse / Kodi / RaiDrive / Explorer)
	if uname, pwd, ok := r.BasicAuth(); ok && uname != "" {
		if u, err := store.GetUserByUsername(ctx, uname); err == nil && !u.MustResetPassword {
			if VerifyPassword(u.PasswordHash, pwd) {
				return u
			}
		}
	}

	return nil
}

// IssueSessionCookie creates a 30-day session in SQLite and sets HttpOnly cookie.
func IssueSessionCookie(ctx context.Context, w http.ResponseWriter, store *db.Store, userID int64) (string, error) {
	tok, err := GenerateSessionToken()
	if err != nil {
		return "", err
	}
	exp := time.Now().Add(30 * 24 * time.Hour)
	if err := store.CreateSession(ctx, tok, userID, exp.Unix()); err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    tok,
		Path:     "/",
		Expires:  exp,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return tok, nil
}
