package tool

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"hubfly-tool-manager/internal/app"
	"hubfly-tool-manager/internal/db"
	"hubfly-tool-manager/internal/model"
	"hubfly-tool-manager/internal/pm2"
)

type Manager struct {
	cfg    model.ManagerConfig
	store  *db.Store
	pm2    *pm2.Client
	runner app.CommandRunner
	logger *log.Logger

	mu sync.Mutex
}

func NewManager(cfg model.ManagerConfig, store *db.Store, pm2c *pm2.Client, logger *log.Logger) *Manager {
	return &Manager{
		cfg:    cfg,
		store:  store,
		pm2:    pm2c,
		runner: app.CommandRunner{Timeout: time.Duration(cfg.Manager.CommandTimeoutSecs) * time.Second},
		logger: logger,
	}
}

func (m *Manager) EnsureRuntime() error {
	if err := os.MkdirAll(m.cfg.Manager.DataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	if err := os.MkdirAll(m.cfg.Manager.BackupsDir, 0o755); err != nil {
		return fmt.Errorf("create backups dir: %w", err)
	}
	if err := m.pm2.EnsureInstalled(); err != nil {
		return err
	}
	return nil
}

func (m *Manager) ListStatus() []model.ToolRuntimeStatus {
	out := make([]model.ToolRuntimeStatus, 0, len(m.cfg.Tools))
	for _, t := range m.cfg.Tools {
		out = append(out, m.GetStatus(t.Name))
	}
	return out
}

func (m *Manager) GetStatus(name string) model.ToolRuntimeStatus {
	t, err := m.mustTool(name)
	if err != nil {
		return model.ToolRuntimeStatus{Name: name, Error: err.Error()}
	}

	status := model.ToolRuntimeStatus{Name: t.Name, Version: "unknown"}
	pm2Status, err := m.pm2.Status(t.Name)
	if err != nil {
		status.Error = err.Error()
	} else {
		status.PM2Status = pm2Status
	}

	if v, err := m.GetToolVersion(t); err == nil {
		status.Version = v
	}

	if record, err := m.store.LatestVersion(t.Name); err == nil {
		status.UpdatedAt = record.UpdatedAt
	}

	return status
}

func (m *Manager) Start(name string) error {
	t, err := m.mustTool(name)
	if err != nil {
		return err
	}
	if err := m.pm2.StartOrReload(t); err != nil {
		return err
	}
	return m.pm2.Save()
}

func (m *Manager) Stop(name string) error {
	if _, err := m.mustTool(name); err != nil {
		return err
	}
	if err := m.pm2.Stop(name); err != nil {
		return err
	}
	return m.pm2.Save()
}

func (m *Manager) Restart(name string) error {
	t, err := m.mustTool(name)
	if err != nil {
		return err
	}
	if err := m.pm2.StartOrReload(t); err != nil {
		return err
	}
	return m.pm2.Save()
}

func (m *Manager) Provision(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	t, err := m.mustTool(name)
	if err != nil {
		return err
	}

	if err := m.ensureWorkDir(t); err != nil {
		return err
	}

	if err := m.runCommandInDir(t.WorkDir, t.InstallCommand, "install"); err != nil {
		return err
	}

	if err := m.pm2.StartOrReload(t); err != nil {
		return err
	}
	if err := m.pm2.Save(); err != nil {
		return err
	}

	version, _ := m.GetToolVersion(t)
	commitHash, _ := m.currentCommit(t.WorkDir)
	_ = m.store.InsertVersion(model.VersionRecord{
		ToolName:   t.Name,
		Version:    version,
		UpdatedAt:  time.Now().UTC(),
		CommitHash: commitHash,
		Notes:      "provision",
	})
	return nil
}

func (m *Manager) Update(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	t, err := m.mustTool(name)
	if err != nil {
		return err
	}
	if err := m.ensureWorkDir(t); err != nil {
		return err
	}

	backupDir, err := m.backupToolFiles(t)
	if err != nil {
		return err
	}

	if t.Repo != "" {
		if _, err := os.Stat(filepath.Join(t.WorkDir, ".git")); err == nil {
			if _, err := m.runner.Run(m.cfg.Manager.GitBin, "-C", t.WorkDir, "fetch", "--all", "--prune"); err != nil {
				return fmt.Errorf("git fetch failed: %w", err)
			}
			if _, err := m.runner.Run(m.cfg.Manager.GitBin, "-C", t.WorkDir, "checkout", t.Branch); err != nil {
				return fmt.Errorf("git checkout failed: %w", err)
			}
			if _, err := m.runner.Run(m.cfg.Manager.GitBin, "-C", t.WorkDir, "pull", "--ff-only", "origin", t.Branch); err != nil {
				return fmt.Errorf("git pull failed: %w", err)
			}
		}
	}

	if err := m.runCommandInDir(t.WorkDir, t.UpdateCommand, "update"); err != nil {
		return err
	}

	if err := m.pm2.StartOrReload(t); err != nil {
		return err
	}
	if err := m.pm2.Save(); err != nil {
		return err
	}

	if err := m.trimBackups(t.Name, 3); err != nil {
		m.logger.Printf("backup trim warning for %s: %v", t.Name, err)
	}

	version, _ := m.GetToolVersion(t)
	if version == "" {
		version = "unknown"
	}
	commitHash, _ := m.currentCommit(t.WorkDir)
	if err := m.store.InsertVersion(model.VersionRecord{
		ToolName:   t.Name,
		Version:    version,
		UpdatedAt:  time.Now().UTC(),
		CommitHash: commitHash,
		Notes:      fmt.Sprintf("backup=%s", backupDir),
	}); err != nil {
		return err
	}
	return nil
}

// SelfUpdate updates manager source/workdir and optionally runs configured update command.
// By design it does not write to tool_versions table.
func (m *Manager) SelfUpdate(workDir string, updateCommand []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if workDir == "" {
		return errors.New("self-update workDir is required")
	}

	if _, err := os.Stat(filepath.Join(workDir, ".git")); err == nil {
		if _, err := m.runner.Run(m.cfg.Manager.GitBin, "-C", workDir, "pull", "--ff-only"); err != nil {
			return fmt.Errorf("self-update git pull failed: %w", err)
		}
	}

	if len(updateCommand) > 0 {
		if _, err := m.runRawInDir(workDir, updateCommand); err != nil {
			return fmt.Errorf("self update command failed: %w", err)
		}
	}
	return nil
}

func (m *Manager) History(name string, limit int) ([]model.VersionRecord, error) {
	if _, err := m.mustTool(name); err != nil {
		return nil, err
	}
	return m.store.ListVersions(name, limit)
}

func (m *Manager) ListBackups(name string) ([]model.BackupSnapshot, error) {
	if _, err := m.mustTool(name); err != nil {
		return nil, err
	}
	return m.listBackups(name)
}

func (m *Manager) Rollback(name, backupID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	t, err := m.mustTool(name)
	if err != nil {
		return err
	}

	backups, err := m.listBackups(name)
	if err != nil {
		return err
	}
	if len(backups) == 0 {
		return fmt.Errorf("no backups available for %s", name)
	}

	selected := backups[0]
	if backupID != "" {
		found := false
		for _, b := range backups {
			if b.ID == backupID {
				selected = b
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("backup id not found for %s: %s", name, backupID)
		}
	}

	safeguardDir, err := m.backupToolFiles(t)
	if err != nil {
		return fmt.Errorf("create pre-rollback backup: %w", err)
	}

	if err := m.pm2.Stop(t.Name); err != nil {
		return err
	}

	if err := m.restoreToolFromBackup(t, selected.Path); err != nil {
		return fmt.Errorf("restore from backup %s: %w", selected.ID, err)
	}

	if err := m.pm2.StartOrReload(t); err != nil {
		return err
	}
	if err := m.pm2.Save(); err != nil {
		return err
	}

	if err := m.trimBackups(t.Name, 3); err != nil {
		m.logger.Printf("backup trim warning for %s: %v", t.Name, err)
	}

	version, _ := m.GetToolVersion(t)
	if version == "" {
		version = "unknown"
	}
	commitHash, _ := m.currentCommit(t.WorkDir)
	_ = m.store.InsertVersion(model.VersionRecord{
		ToolName:   t.Name,
		Version:    version,
		UpdatedAt:  time.Now().UTC(),
		CommitHash: commitHash,
		Notes:      fmt.Sprintf("rollback_from=%s safeguard=%s", selected.Path, safeguardDir),
	})
	return nil
}

func (m *Manager) GetToolVersion(t model.ToolConfig) (string, error) {
	if len(t.VersionCommand) == 0 {
		return "unknown", nil
	}
	res, err := m.runner.Run(t.VersionCommand[0], t.VersionCommand[1:]...)
	if err != nil {
		return "unknown", nil
	}
	if res.Stdout == "" {
		return "unknown", nil
	}
	return firstLine(res.Stdout), nil
}

func (m *Manager) mustTool(name string) (model.ToolConfig, error) {
	for _, t := range m.cfg.Tools {
		if t.Name == name {
			return t, nil
		}
	}
	return model.ToolConfig{}, fmt.Errorf("unknown tool: %s", name)
}

func (m *Manager) ensureWorkDir(t model.ToolConfig) error {
	if _, err := os.Stat(t.WorkDir); err == nil {
		return nil
	}
	if t.Repo == "" {
		return fmt.Errorf("workdir missing and repo not configured for %s", t.Name)
	}
	if err := os.MkdirAll(filepath.Dir(t.WorkDir), 0o755); err != nil {
		return fmt.Errorf("prepare parent dir: %w", err)
	}
	if _, err := m.runner.Run(m.cfg.Manager.GitBin, "clone", "-b", t.Branch, t.Repo, t.WorkDir); err != nil {
		return fmt.Errorf("clone repo for %s: %w", t.Name, err)
	}
	return nil
}

func (m *Manager) runCommandInDir(workDir string, cmd []string, label string) error {
	if len(cmd) == 0 {
		return nil
	}
	_, err := m.runRawInDir(workDir, cmd)
	if err != nil {
		return fmt.Errorf("%s command failed: %w", label, err)
	}
	return nil
}

func (m *Manager) runRawInDir(workDir string, cmd []string) (app.Result, error) {
	if len(cmd) == 0 {
		return app.Result{}, nil
	}
	if len(cmd[0]) == 0 {
		return app.Result{}, errors.New("command executable is empty")
	}
	if cmd[0] == "git" {
		return m.runner.RunInDir(workDir, m.cfg.Manager.GitBin, cmd[1:]...)
	}
	return m.runner.RunInDir(workDir, cmd[0], cmd[1:]...)
}

func (m *Manager) backupToolFiles(t model.ToolConfig) (string, error) {
	now := time.Now().UTC().Format("20060102T150405Z")
	dir := filepath.Join(m.cfg.Manager.BackupsDir, t.Name, now)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}

	copyIfExists := func(src, dstName string) error {
		if src == "" {
			return nil
		}
		if _, err := os.Stat(src); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		dst := filepath.Join(dir, dstName)
		return copyPath(src, dst)
	}

	if err := copyIfExists(t.BinaryPath, "binary"); err != nil {
		return "", fmt.Errorf("backup binary: %w", err)
	}
	if err := copyIfExists(t.ConfigFile, "config.json"); err != nil {
		return "", fmt.Errorf("backup config file: %w", err)
	}
	if err := copyIfExists(t.EnvFile, ".env"); err != nil {
		return "", fmt.Errorf("backup env file: %w", err)
	}
	if err := copyIfExists(t.ConfigsDir, "configs"); err != nil {
		return "", fmt.Errorf("backup configs dir: %w", err)
	}

	return dir, nil
}

func (m *Manager) trimBackups(toolName string, keep int) error {
	if keep <= 0 {
		keep = 1
	}
	list, err := m.listBackups(toolName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(list) <= keep {
		return nil
	}
	for _, b := range list[keep:] {
		if err := os.RemoveAll(b.Path); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) listBackups(toolName string) ([]model.BackupSnapshot, error) {
	root := filepath.Join(m.cfg.Manager.BackupsDir, toolName)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}

	list := make([]model.BackupSnapshot, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		list = append(list, model.BackupSnapshot{
			ID:        e.Name(),
			ToolName:  toolName,
			Path:      filepath.Join(root, e.Name()),
			CreatedAt: info.ModTime().UTC(),
		})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.After(list[j].CreatedAt) })
	return list, nil
}

func (m *Manager) currentCommit(workDir string) (string, error) {
	if _, err := os.Stat(filepath.Join(workDir, ".git")); err != nil {
		return "", err
	}
	res, err := m.runner.Run(m.cfg.Manager.GitBin, "-C", workDir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(res.Stdout), nil
}

func firstLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return "unknown"
	}
	return strings.TrimSpace(lines[0])
}

func copyPath(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return copyDir(src, dst)
	}
	return copyFile(src, dst, info.Mode())
}

func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src, dst string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}

func (m *Manager) restoreToolFromBackup(t model.ToolConfig, backupDir string) error {
	restoreFileIfExists := func(src, dst string) error {
		if dst == "" {
			return nil
		}
		if _, err := os.Stat(src); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		return copyPath(src, dst)
	}
	restoreDirIfExists := func(src, dst string) error {
		if dst == "" {
			return nil
		}
		if _, err := os.Stat(src); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		_ = os.RemoveAll(dst)
		return copyPath(src, dst)
	}

	if err := restoreFileIfExists(filepath.Join(backupDir, "binary"), t.BinaryPath); err != nil {
		return fmt.Errorf("restore binary: %w", err)
	}
	if err := restoreFileIfExists(filepath.Join(backupDir, "config.json"), t.ConfigFile); err != nil {
		return fmt.Errorf("restore config file: %w", err)
	}
	if err := restoreFileIfExists(filepath.Join(backupDir, ".env"), t.EnvFile); err != nil {
		return fmt.Errorf("restore env file: %w", err)
	}
	if err := restoreDirIfExists(filepath.Join(backupDir, "configs"), t.ConfigsDir); err != nil {
		return fmt.Errorf("restore configs dir: %w", err)
	}
	return nil
}
