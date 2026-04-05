package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"hubfly-tool-manager/internal/model"
	"hubfly-tool-manager/internal/tool"
	"hubfly-tool-manager/internal/version"
)

type Server struct {
	manager      *tool.Manager
	logger       *log.Logger
	mux          *http.ServeMux
	tokenFile    string
	lockdownFile string
	authMu       sync.Mutex
	sessionsMu   sync.Mutex
	sessionSecret []byte
	sessions     map[string]authSession
}

func New(manager *tool.Manager, logger *log.Logger, tokenFile, lockdownFile, sessionSecretFile string) *Server {
	s := &Server{manager: manager, logger: logger, mux: http.NewServeMux(), tokenFile: tokenFile, lockdownFile: lockdownFile}
	if err := s.initSessionSecurity(sessionSecretFile); err != nil {
		logger.Fatalf("init session security: %v", err)
	}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	return s.recoverMiddleware(s.logMiddleware(s.authMiddleware(s.mux)))
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /version", s.handleManagerVersion)
	s.mux.HandleFunc("GET /auth/status", s.handleAuthStatus)
	s.mux.HandleFunc("POST /auth/login", s.handleLogin)
	s.mux.HandleFunc("POST /auth/logout", s.handleLogout)
	s.mux.HandleFunc("GET /web", s.handleWeb)
	s.mux.HandleFunc("GET /web/", s.handleWeb)
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("POST /tools/register", s.handleRegister)
	s.mux.HandleFunc("GET /tools", s.handleListTools)
	s.mux.HandleFunc("POST /tools/update-all", s.handleUpdateAll)
	s.mux.HandleFunc("GET /tools/release-suggestions/{name}", s.handleReleaseSuggestion)
	s.mux.HandleFunc("GET /tools/{name}", s.handleToolStatus)
	s.mux.HandleFunc("GET /tools/{name}/history", s.handleHistory)
	s.mux.HandleFunc("GET /tools/{name}/backups", s.handleBackups)
	s.mux.HandleFunc("GET /tools/{name}/version", s.handleVersion)
	s.mux.HandleFunc("GET /tools/{name}/logs", s.handleToolLogs)
	s.mux.HandleFunc("POST /tools/{name}/logs/cleanup", s.handleToolLogCleanup)
	s.mux.HandleFunc("POST /tools/{name}/start", s.handleStart)
	s.mux.HandleFunc("POST /tools/{name}/stop", s.handleStop)
	s.mux.HandleFunc("POST /tools/{name}/restart", s.handleRestart)
	s.mux.HandleFunc("POST /tools/{name}/provision", s.handleProvision)
	s.mux.HandleFunc("POST /tools/{name}/update", s.handleUpdate)
	s.mux.HandleFunc("POST /tools/{name}/configure-update", s.handleConfigureUpdate)
	s.mux.HandleFunc("POST /tools/{name}/rollback", s.handleRollback)
	s.mux.HandleFunc("POST /tools/{name}/cleanup", s.handleCleanup)
	s.mux.HandleFunc("GET /logs", s.handleLogs)
	s.mux.HandleFunc("POST /logs/cleanup", s.handleLogsCleanup)
	s.mux.HandleFunc("POST /self/update", s.handleSelfUpdate)
}

func (s *Server) handleManagerVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"version": version.ManagerVersion})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"time":    time.Now().UTC(),
		"version": version.ManagerVersion,
	})
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.isPublicRoute(r) {
			next.ServeHTTP(w, r)
			return
		}

		state, err := s.loadLockdownState()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read lockdown state")
			return
		}
		if state.Locked || (!state.LockedUntil.IsZero() && time.Now().UTC().Before(state.LockedUntil)) {
			writeError(w, http.StatusLocked, "service is in lockdown mode; run `htm unlock` locally")
			return
		}

		if strings.TrimSpace(extractToken(r)) != "" {
			if s.authorizeBearerToken(w, r) {
				next.ServeHTTP(w, r)
			}
			return
		}
		if s.authorizeSession(w, r) {
			next.ServeHTTP(w, r)
			return
		}
		writeError(w, http.StatusUnauthorized, "authentication required")
	})
}

type lockdownState struct {
	Locked         bool      `json:"locked"`
	FailedAttempts int       `json:"failed_attempts"`
	LockedUntil    time.Time `json:"locked_until,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (s *Server) loadLockdownState() (lockdownState, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	return s.loadLockdownStateUnlocked()
}

func (s *Server) loadLockdownStateUnlocked() (lockdownState, error) {
	var st lockdownState
	if strings.TrimSpace(s.lockdownFile) == "" {
		return st, errors.New("empty lockdown file path")
	}
	b, err := os.ReadFile(s.lockdownFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return lockdownState{}, nil
		}
		return st, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return lockdownState{}, nil
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, err
	}
	return st, nil
}

func (s *Server) currentLockdownState() lockdownState {
	st, _ := s.loadLockdownState()
	return st
}

func (s *Server) isPublicRoute(r *http.Request) bool {
	if r.Method == http.MethodGet && (r.URL.Path == "/version" || r.URL.Path == "/web" || r.URL.Path == "/web/" || r.URL.Path == "/health" || r.URL.Path == "/auth/status") {
		return true
	}
	return r.Method == http.MethodPost && (r.URL.Path == "/auth/login" || r.URL.Path == "/auth/logout")
}

func (s *Server) saveLockdownStateUnlocked(st lockdownState) error {
	if err := os.MkdirAll(filepath.Dir(s.lockdownFile), 0o755); err != nil {
		return err
	}
	tmp := s.lockdownFile + ".tmp"
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.lockdownFile)
}

func (s *Server) recordFailedAttempt(_ string) (lockdownState, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()

	st, err := s.loadLockdownStateUnlocked()
	if err != nil {
		return st, err
	}
	st.FailedAttempts++
	now := time.Now().UTC()
	if st.FailedAttempts >= 5 {
		st.LockedUntil = now.Add(lockdownWindow)
	}
	if st.FailedAttempts >= 10 {
		st.Locked = true
	}
	st.UpdatedAt = now
	if err := s.saveLockdownStateUnlocked(st); err != nil {
		return st, err
	}
	return st, nil
}

func (s *Server) isTemporarilyBlocked(ip string) (lockdownState, bool) {
	st, err := s.loadLockdownState()
	if err != nil {
		return lockdownState{}, false
	}
	if st.Locked {
		return st, true
	}
	_ = ip
	if !st.LockedUntil.IsZero() && time.Now().UTC().Before(st.LockedUntil) {
		return st, true
	}
	return st, false
}

func (s *Server) clearFailedAttempts() error {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	st, err := s.loadLockdownStateUnlocked()
	if err != nil {
		return err
	}
	if st.FailedAttempts == 0 && !st.Locked {
		return nil
	}
	st.FailedAttempts = 0
	st.LockedUntil = time.Time{}
	st.UpdatedAt = time.Now().UTC()
	return s.saveLockdownStateUnlocked(st)
}

func (s *Server) loadToken() (string, error) {
	if strings.TrimSpace(s.tokenFile) == "" {
		return "", errors.New("empty token file path")
	}
	b, err := os.ReadFile(s.tokenFile)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return "", errors.New("empty token")
	}
	return token, nil
}

func extractToken(r *http.Request) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	if v := strings.TrimSpace(r.Header.Get("X-HTM-Token")); v != "" {
		return v
	}
	return ""
}

func (s *Server) authorizeBearerToken(w http.ResponseWriter, r *http.Request) bool {
	got := strings.TrimSpace(extractToken(r))
	if got == "" {
		return false
	}
	expected, err := s.loadToken()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "security token not initialized; run `htm init`")
		return false
	}
	if subtleCompare(got, expected) {
		if err := s.clearFailedAttempts(); err != nil {
			s.logger.Printf("warning: failed to clear failed auth attempts: %v", err)
		}
		return true
	}
	if st, err := s.recordFailedAttempt(remoteIP(r)); err == nil && st.Locked {
		writeError(w, http.StatusLocked, "service entered lockdown mode after repeated invalid token attempts; run `htm unlock` locally")
		return false
	}
	writeError(w, http.StatusUnauthorized, "invalid security token")
	return false
}

func (s *Server) authorizeSession(w http.ResponseWriter, r *http.Request) bool {
	_, sess := s.sessionFromRequest(r)
	if sess == nil {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if subtleCompare(strings.TrimSpace(r.Header.Get("X-CSRF-Token")), sess.CSRFToken) == false {
			writeError(w, http.StatusForbidden, "missing or invalid csrf token")
			return false
		}
	}
	return true
}

func subtleCompare(a, b string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req model.RegisterToolRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	toolCfg, err := s.manager.RegisterTool(req)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "is required") || strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "checksum mismatch") {
			status = http.StatusBadRequest
		}
		if strings.Contains(err.Error(), "UNIQUE") {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "tool": toolCfg})
}

func (s *Server) handleListTools(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"tools": s.manager.ListStatus()})
}

func (s *Server) handleUpdateAll(w http.ResponseWriter, _ *http.Request) {
	results := s.manager.UpdateAll()
	ok := true
	for _, result := range results {
		if !result.OK {
			ok = false
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      ok,
		"results": results,
	})
}

func (s *Server) handleToolStatus(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cfg, err := s.manager.GetTool(name)
	if err != nil {
		if strings.Contains(err.Error(), "unknown tool") {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	status := s.manager.GetStatus(name)
	status.Release = s.manager.SuggestRelease(name)
	writeJSON(w, http.StatusOK, map[string]any{
		"name":            status.Name,
		"slug":            cfg.Slug,
		"pm2_status":      status.PM2Status,
		"version":         status.Version,
		"updated_at":      status.UpdatedAt,
		"error":           status.Error,
		"tool_dir":        cfg.ToolDir,
		"binary_path":     cfg.BinaryPath,
		"download_url":    cfg.DownloadURL,
		"checksum":        cfg.Checksum,
		"args":            cfg.Args,
		"version_command": cfg.VersionCommand,
		"created_at":      cfg.CreatedAt,
		"db_updated_at":   cfg.UpdatedAt,
		"logs":            status.Logs,
		"release":         status.Release,
	})
}

func (s *Server) handleReleaseSuggestion(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	writeJSON(w, http.StatusOK, map[string]any{
		"tool":    name,
		"release": s.manager.SuggestRelease(name),
	})
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		i, err := strconv.Atoi(v)
		if err != nil || i <= 0 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		limit = i
	}

	h, err := s.manager.History(name, limit)
	if err != nil {
		if strings.Contains(err.Error(), "unknown tool") {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": h})
}

func (s *Server) handleBackups(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	backups, err := s.manager.ListBackups(name)
	if err != nil {
		if strings.Contains(err.Error(), "unknown tool") {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusOK, map[string]any{"backups": []any{}})
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backups": backups})
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	status := s.manager.GetStatus(name)
	if status.Error != "" && strings.Contains(status.Error, "unknown tool") {
		writeError(w, http.StatusNotFound, status.Error)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": status.Name, "version": status.Version})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		limit = n
	}
	entries, err := s.manager.SearchLogs(
		r.URL.Query().Get("tool"),
		r.URL.Query().Get("file"),
		r.URL.Query().Get("q"),
		limit,
	)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "unknown tool") {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": entries})
}

func (s *Server) handleToolLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 200
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		limit = n
	}
	entries, err := s.manager.SearchLogs(r.PathValue("name"), q.Get("file"), q.Get("q"), limit)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "unknown tool") {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": entries})
}

type cleanupLogsRequest struct {
	File string `json:"file"`
	Tool string `json:"tool"`
}

func (s *Server) handleLogsCleanup(w http.ResponseWriter, r *http.Request) {
	var req cleanupLogsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if err := s.manager.CleanupLogs(req.Tool, req.File); err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "unknown tool") {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tool": req.Tool, "file": req.File})
}

func (s *Server) handleToolLogCleanup(w http.ResponseWriter, r *http.Request) {
	var req cleanupLogsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	name := r.PathValue("name")
	if err := s.manager.CleanupLogs(name, req.File); err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "unknown tool") {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tool": name, "file": req.File})
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	s.handleAction(w, r, s.manager.Start)
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	s.handleAction(w, r, s.manager.Stop)
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	s.handleAction(w, r, s.manager.Restart)
}

func (s *Server) handleProvision(w http.ResponseWriter, r *http.Request) {
	s.handleAction(w, r, s.manager.Provision)
}

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	s.handleAction(w, r, s.manager.Update)
}

func (s *Server) handleConfigureUpdate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req model.ConfigureToolRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if err := s.manager.ConfigureAndUpdate(name, req); err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "unknown tool") {
			status = http.StatusNotFound
		} else if strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "no configuration changes") || strings.Contains(err.Error(), "cannot be empty") {
			status = http.StatusBadRequest
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tool": name, "updated": true})
}

type rollbackRequest struct {
	BackupID string `json:"backup_id"`
}

func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req rollbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if err := s.manager.Rollback(name, req.BackupID); err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "unknown tool") {
			status = http.StatusNotFound
		} else if strings.Contains(err.Error(), "backup id not found") || strings.Contains(err.Error(), "no backups available") {
			status = http.StatusBadRequest
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tool": name, "backup_id": req.BackupID})
}

func (s *Server) handleCleanup(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.manager.CleanupTool(name); err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "unknown tool") {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tool": name})
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request, fn func(string) error) {
	name := r.PathValue("name")
	if err := fn(name); err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "unknown tool") {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tool": name})
}

func (s *Server) handleSelfUpdate(w http.ResponseWriter, r *http.Request) {
	if err := json.NewDecoder(r.Body).Decode(&map[string]any{}); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	// Self-update can restart this same service; run it asynchronously so client gets a response first.
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": "self-update accepted"})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	go func() {
		time.Sleep(300 * time.Millisecond)
		if err := s.manager.SelfUpdate(); err != nil {
			s.logger.Printf("self-update failed: %v", err)
		}
	}()
}

func (s *Server) logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.logger.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

func (s *Server) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.logger.Printf("panic recovered for %s %s: %v", r.Method, r.URL.Path, rec)
				writeError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func ListenAndServe(addr string, handler http.Handler, logger *log.Logger) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	logger.Printf("http server listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil {
		return fmt.Errorf("listen and serve: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": msg})
}
