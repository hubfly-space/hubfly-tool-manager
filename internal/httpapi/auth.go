package httpapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	sessionCookieName   = "htm_session"
	sessionTTL          = 12 * time.Hour
	lockdownWindow      = 15 * time.Minute
	sessionCleanupEvery = 30 * time.Minute
)

type authSession struct {
	ID        string
	CSRFToken string
	ExpiresAt time.Time
}

type loginRequest struct {
	Token string `json:"token"`
}

func (s *Server) initSessionSecurity(secretFile string) error {
	secret, err := loadOrCreateSecret(secretFile)
	if err != nil {
		return err
	}
	s.sessionSecret = secret
	s.sessions = map[string]authSession{}
	go s.cleanupSessionsLoop()
	return nil
}

func loadOrCreateSecret(path string) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("session secret file path is empty")
	}
	if data, err := os.ReadFile(path); err == nil {
		decoded, decErr := hex.DecodeString(strings.TrimSpace(string(data)))
		if decErr == nil && len(decoded) >= 32 {
			return decoded, nil
		}
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("generate session secret: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create session secret dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(buf)), 0o600); err != nil {
		return nil, fmt.Errorf("write session secret: %w", err)
	}
	return buf, nil
}

func (s *Server) cleanupSessionsLoop() {
	ticker := time.NewTicker(sessionCleanupEvery)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now().UTC()
		s.sessionsMu.Lock()
		for key, sess := range s.sessions {
			if now.After(sess.ExpiresAt) {
				delete(s.sessions, key)
			}
		}
		s.sessionsMu.Unlock()
	}
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	_, sess := s.sessionFromRequest(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": sess != nil,
		"token_ready":   s.tokenReady(),
		"lockdown":      s.currentLockdownState(),
		"csrf_token":    csrfValue(sess),
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if state, blocked := s.isTemporarilyBlocked(remoteIP(r)); blocked {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"ok":            false,
			"error":         "too many failed login attempts",
			"retry_after_s": int(time.Until(state.LockedUntil).Seconds()),
		})
		return
	}

	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	expected, err := s.loadToken()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "security token not initialized; run `htm init`")
		return
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(req.Token)), []byte(expected)) != 1 {
		state, _ := s.recordFailedAttempt(remoteIP(r))
		status := http.StatusUnauthorized
		if state.Locked {
			status = http.StatusLocked
		}
		if !state.Locked && !state.LockedUntil.IsZero() && time.Now().UTC().Before(state.LockedUntil) {
			status = http.StatusTooManyRequests
		}
		writeError(w, status, "invalid security token")
		return
	}

	if err := s.clearFailedAttempts(); err != nil {
		s.logger.Printf("warning: failed to clear auth attempts: %v", err)
	}
	session, err := s.newSession()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return
	}
	http.SetCookie(w, s.sessionCookie(session, r.TLS != nil))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"authenticated":  true,
		"csrf_token":     session.CSRFToken,
		"expires_at":     session.ExpiresAt,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if signed := strings.TrimSpace(cookieValue(r, sessionCookieName)); signed != "" {
		s.sessionsMu.Lock()
		delete(s.sessions, signed)
		s.sessionsMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) tokenReady() bool {
	_, err := s.loadToken()
	return err == nil
}

func csrfValue(sess *authSession) string {
	if sess == nil {
		return ""
	}
	return sess.CSRFToken
}

func (s *Server) newSession() (authSession, error) {
	id, err := randomToken(32)
	if err != nil {
		return authSession{}, err
	}
	csrf, err := randomToken(24)
	if err != nil {
		return authSession{}, err
	}
	session := authSession{
		ID:        id,
		CSRFToken: csrf,
		ExpiresAt: time.Now().UTC().Add(sessionTTL),
	}
	signed := s.signSession(id)
	s.sessionsMu.Lock()
	s.sessions[signed] = session
	s.sessionsMu.Unlock()
	return session, nil
}

func (s *Server) sessionCookie(sess authSession, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    s.signSession(sess.ID),
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		Expires:  sess.ExpiresAt,
	}
}

func (s *Server) signSession(id string) string {
	mac := hmac.New(sha256.New, s.sessionSecret)
	_, _ = mac.Write([]byte(id))
	sig := hex.EncodeToString(mac.Sum(nil))
	return id + "." + sig
}

func (s *Server) validateSignedSession(value string) (string, bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return "", false
	}
	expected := s.signSession(parts[0])
	if subtle.ConstantTimeCompare([]byte(value), []byte(expected)) != 1 {
		return "", false
	}
	return parts[0], true
}

func (s *Server) sessionFromRequest(r *http.Request) (string, *authSession) {
	signed := strings.TrimSpace(cookieValue(r, sessionCookieName))
	if signed == "" {
		return "", nil
	}
	if _, ok := s.validateSignedSession(signed); !ok {
		return "", nil
	}
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	sess, ok := s.sessions[signed]
	if !ok {
		return "", nil
	}
	if time.Now().UTC().After(sess.ExpiresAt) {
		delete(s.sessions, signed)
		return "", nil
	}
	return signed, &sess
}

func cookieValue(r *http.Request, name string) string {
	cookie, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func randomToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}
