package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"hubfly-tool-manager/internal/model"
	"hubfly-tool-manager/internal/tool"
)

type Server struct {
	manager *tool.Manager
	logger  *log.Logger
	mux     *http.ServeMux
}

func New(manager *tool.Manager, logger *log.Logger) *Server {
	s := &Server{manager: manager, logger: logger, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	return s.recoverMiddleware(s.logMiddleware(s.mux))
}

func (s *Server) routes() {
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
	s.mux.HandleFunc("POST /tools/{name}/rollback", s.handleRollback)
	s.mux.HandleFunc("POST /tools/{name}/cleanup", s.handleCleanup)
	s.mux.HandleFunc("POST /self/update", s.handleSelfUpdate)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "time": time.Now().UTC()})
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

type selfUpdateRequest struct {
	WorkDir       string   `json:"work_dir"`
	UpdateCommand []string `json:"update_command"`
}

func (s *Server) handleSelfUpdate(w http.ResponseWriter, r *http.Request) {
	var req selfUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if err := s.manager.SelfUpdate(req.WorkDir, req.UpdateCommand); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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
