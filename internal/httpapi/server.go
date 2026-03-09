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
}

func New(manager *tool.Manager, logger *log.Logger, tokenFile, lockdownFile string) *Server {
	s := &Server{manager: manager, logger: logger, mux: http.NewServeMux(), tokenFile: tokenFile, lockdownFile: lockdownFile}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	return s.recoverMiddleware(s.logMiddleware(s.authMiddleware(s.mux)))
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /version", s.handleManagerVersion)
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("POST /tools/register", s.handleRegister)
	s.mux.HandleFunc("GET /tools", s.handleListTools)
	s.mux.HandleFunc("GET /tools/{name}", s.handleToolStatus)
	s.mux.HandleFunc("GET /tools/{name}/history", s.handleHistory)
	s.mux.HandleFunc("GET /tools/{name}/backups", s.handleBackups)
	s.mux.HandleFunc("GET /tools/{name}/version", s.handleVersion)
	s.mux.HandleFunc("POST /tools/{name}/start", s.handleStart)
	s.mux.HandleFunc("POST /tools/{name}/stop", s.handleStop)
	s.mux.HandleFunc("POST /tools/{name}/restart", s.handleRestart)
	s.mux.HandleFunc("POST /tools/{name}/provision", s.handleProvision)
	s.mux.HandleFunc("POST /tools/{name}/update", s.handleUpdate)
	s.mux.HandleFunc("POST /tools/{name}/configure-update", s.handleConfigureUpdate)
	s.mux.HandleFunc("POST /tools/{name}/rollback", s.handleRollback)
	s.mux.HandleFunc("POST /tools/{name}/cleanup", s.handleCleanup)
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
		// Single public endpoint by design.
		if r.Method == http.MethodGet && r.URL.Path == "/version" {
			next.ServeHTTP(w, r)
			return
		}

		state, err := s.loadLockdownState()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read lockdown state")
			return
		}
		if state.Locked {
			writeError(w, http.StatusLocked, "service is in lockdown mode; run `htm unlock` locally")
			return
		}

		expected, err := s.loadToken()
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "security token not initialized; run `htm init`")
			return
		}
		got := strings.TrimSpace(extractToken(r))
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
			// Count failed attempts only when a token is provided but invalid.
			if got != "" {
				if st, err := s.recordFailedAttempt(); err == nil && st.Locked {
					writeError(w, http.StatusLocked, "service entered lockdown mode after repeated invalid token attempts; run `htm unlock` locally")
					return
				}
			}
			writeError(w, http.StatusUnauthorized, "invalid or missing security token")
			return
		}

		if err := s.clearFailedAttempts(); err != nil {
			s.logger.Printf("warning: failed to clear failed auth attempts: %v", err)
		}
		next.ServeHTTP(w, r)
	})
}

type lockdownState struct {
	Locked         bool      `json:"locked"`
	FailedAttempts int       `json:"failed_attempts"`
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

func (s *Server) recordFailedAttempt() (lockdownState, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()

	st, err := s.loadLockdownStateUnlocked()
	if err != nil {
		return st, err
	}
	st.FailedAttempts++
	if st.FailedAttempts >= 10 {
		st.Locked = true
	}
	st.UpdatedAt = time.Now().UTC()
	if err := s.saveLockdownStateUnlocked(st); err != nil {
		return st, err
	}
	return st, nil
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

func (s *Server) handleToolStatus(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	status := s.manager.GetStatus(name)
	if status.Error != "" && strings.Contains(status.Error, "unknown tool") {
		writeError(w, http.StatusNotFound, status.Error)
		return
	}
	writeJSON(w, http.StatusOK, status)
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
