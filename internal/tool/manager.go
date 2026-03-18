package tool

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"hubfly-tool-manager/internal/app"
	"hubfly-tool-manager/internal/db"
	"hubfly-tool-manager/internal/model"
	"hubfly-tool-manager/internal/pm2"
)

const (
	backupRetention   = 3
	httpRetryAttempts = 4
	httpRetryBaseWait = 2 * time.Second
)

var outboundHTTPClient = &http.Client{
	Timeout: 40 * time.Second,
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 12 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   12 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		IdleConnTimeout:       60 * time.Second,
	},
}

type Manager struct {
	cfg    model.RuntimeConfig
	store  *db.Store
	pm2    *pm2.Client
	runner app.CommandRunner
	logger *log.Logger

	mu sync.Mutex
}

func NewManager(cfg model.RuntimeConfig, store *db.Store, pm2c *pm2.Client, logger *log.Logger) *Manager {
	return &Manager{
		cfg:    cfg,
		store:  store,
		pm2:    pm2c,
		runner: app.CommandRunner{Timeout: time.Duration(cfg.CommandTimeoutSecs) * time.Second},
		logger: logger,
	}
}

func (m *Manager) EnsureRuntime() error {
	for _, dir := range []string{m.cfg.DataDir, m.cfg.BackupsDir, m.cfg.ToolsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create runtime dir %s: %w", dir, err)
		}
	}
	if err := m.pm2.EnsureInstalled(); err != nil {
		return err
	}
	return nil
}

func (m *Manager) EnsureAllRegisteredRunning() {
	tools, err := m.store.ListTools()
	if err != nil {
		m.logger.Printf("list tools for boot reconciliation failed: %v", err)
		return
	}
	for _, t := range tools {
		status, statusErr := m.pm2.Status(t.Name)
		if statusErr != nil {
			m.logger.Printf("boot status check warning for %s: %v", t.Name, statusErr)
		}
		if status == "online" || status == "launching" {
			m.logger.Printf("boot reconciliation: %s already %s", t.Name, status)
			continue
		}
		if err := m.Start(t.Name); err != nil {
			m.logger.Printf("boot reconcile warning for %s: %v", t.Name, err)
			continue
		}
		m.logger.Printf("boot reconciliation: started %s", t.Name)
	}
}

func (m *Manager) RegisterTool(req model.RegisterToolRequest) (model.ToolConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	name := strings.TrimSpace(req.Name)
	if name == "" {
		return model.ToolConfig{}, errors.New("name is required")
	}
	downloadURL := strings.TrimSpace(req.DownloadURL)
	if downloadURL == "" {
		return model.ToolConfig{}, errors.New("download_url is required")
	}
	if _, err := url.ParseRequestURI(downloadURL); err != nil {
		return model.ToolConfig{}, fmt.Errorf("invalid download_url: %w", err)
	}

	slug := slugifyName(name)
	toolDir := filepath.Join(m.cfg.ToolsDir, slug)
	binaryPath := filepath.Join(toolDir, binaryNameFromURL(downloadURL, slug))

	t := model.ToolConfig{
		Name:           name,
		Slug:           slug,
		ToolDir:        toolDir,
		BinaryPath:     binaryPath,
		DownloadURL:    downloadURL,
		Checksum:       strings.TrimSpace(req.Checksum),
		Args:           cleanArgs(req.Args),
		VersionCommand: cleanArgs(req.VersionCommand),
	}

	if err := os.MkdirAll(toolDir, 0o755); err != nil {
		return model.ToolConfig{}, fmt.Errorf("create tool dir: %w", err)
	}
	if err := m.downloadBinary(t.DownloadURL, t.BinaryPath, t.Checksum); err != nil {
		return model.ToolConfig{}, err
	}

	if err := m.store.CreateTool(t); err != nil {
		return model.ToolConfig{}, err
	}

	created, err := m.store.GetTool(t.Name)
	if err != nil {
		return model.ToolConfig{}, err
	}
	return created, nil
}

func (m *Manager) CleanupTool(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	t, err := m.mustTool(name)
	if err != nil {
		return err
	}

	_ = m.pm2.Stop(t.Name)
	_ = m.pm2.Delete(t.Name)
	_ = m.pm2.Save()

	if err := os.RemoveAll(t.ToolDir); err != nil {
		return fmt.Errorf("remove tool dir: %w", err)
	}
	if err := os.RemoveAll(filepath.Join(m.cfg.BackupsDir, t.Slug)); err != nil {
		return fmt.Errorf("remove backup dir: %w", err)
	}
	if err := m.store.DeleteVersions(t.Name); err != nil {
		return err
	}
	if err := m.store.DeleteTool(t.Name); err != nil {
		return err
	}
	return nil
}

func (m *Manager) ListStatus() []model.ToolRuntimeStatus {
	tools, err := m.store.ListTools()
	if err != nil {
		return []model.ToolRuntimeStatus{{Name: "", Error: err.Error()}}
	}
	out := make([]model.ToolRuntimeStatus, 0, len(tools))
	for _, t := range tools {
		if normalized, nerr := m.normalizeToolPaths(t); nerr == nil {
			t = normalized
		} else {
			m.logger.Printf("path normalization warning for %s: %v", t.Name, nerr)
		}
		out = append(out, m.getStatusForTool(t))
	}
	return out
}

func (m *Manager) GetStatus(name string) model.ToolRuntimeStatus {
	t, err := m.mustTool(name)
	if err != nil {
		return model.ToolRuntimeStatus{Name: name, Error: err.Error()}
	}
	return m.getStatusForTool(t)
}

func (m *Manager) GetTool(name string) (model.ToolConfig, error) {
	return m.mustTool(name)
}

func (m *Manager) getStatusForTool(t model.ToolConfig) model.ToolRuntimeStatus {
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
	if err := m.ensureBinary(t); err != nil {
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
	if err := m.ensureBinary(t); err != nil {
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
	if err := m.ensureBinary(t); err != nil {
		return err
	}
	if err := m.pm2.StartOrReload(t); err != nil {
		return err
	}
	if err := m.pm2.Save(); err != nil {
		return err
	}
	version, _ := m.GetToolVersion(t)
	_ = m.store.InsertVersion(model.VersionRecord{ToolName: t.Name, Version: versionOrUnknown(version), UpdatedAt: time.Now().UTC(), Notes: "provision"})
	return nil
}

func (m *Manager) Update(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	t, err := m.mustTool(name)
	if err != nil {
		return err
	}
	return m.performToolUpdateLocked(t, t, "manual_update")
}

func (m *Manager) ConfigureAndUpdate(name string, req model.ConfigureToolRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	current, err := m.mustTool(name)
	if err != nil {
		return err
	}

	next := current
	changed := false
	if req.DownloadURL != nil {
		v := strings.TrimSpace(*req.DownloadURL)
		if v == "" {
			return errors.New("download_url cannot be empty")
		}
		if _, err := url.ParseRequestURI(v); err != nil {
			return fmt.Errorf("invalid download_url: %w", err)
		}
		next.DownloadURL = v
		next.BinaryPath = filepath.Join(next.ToolDir, binaryNameFromURL(v, next.Slug))
		changed = true
	}
	if req.Checksum != nil {
		next.Checksum = strings.TrimSpace(*req.Checksum)
		changed = true
	}
	if req.Args != nil {
		next.Args = cleanArgs(*req.Args)
		changed = true
	}
	if req.VersionCommand != nil {
		next.VersionCommand = cleanArgs(*req.VersionCommand)
		changed = true
	}

	if !changed {
		return errors.New("no configuration changes provided")
	}
	if err := m.store.UpdateTool(next); err != nil {
		return err
	}

	return m.performToolUpdateLocked(next, current, "config_update")
}

func (m *Manager) performToolUpdateLocked(updateTool model.ToolConfig, backupSource model.ToolConfig, notePrefix string) error {
	if err := m.ensureBinary(backupSource); err != nil {
		return err
	}

	backupDir, err := m.backupToolFiles(backupSource)
	if err != nil {
		return err
	}
	if err := m.downloadBinary(updateTool.DownloadURL, updateTool.BinaryPath, updateTool.Checksum); err != nil {
		return err
	}
	if err := m.pm2.StartOrReload(updateTool); err != nil {
		return err
	}
	if err := m.pm2.Save(); err != nil {
		return err
	}
	if err := m.trimBackups(updateTool.Slug, backupRetention); err != nil {
		m.logger.Printf("backup trim warning for %s: %v", updateTool.Name, err)
	}
	version, _ := m.GetToolVersion(updateTool)
	_ = m.store.InsertVersion(model.VersionRecord{
		ToolName:  updateTool.Name,
		Version:   versionOrUnknown(version),
		UpdatedAt: time.Now().UTC(),
		Notes:     fmt.Sprintf("%s backup=%s", notePrefix, backupDir),
	})
	return nil
}

func (m *Manager) SelfUpdate() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	repo := "hubfly-space/hubfly-tool-manager"
	tag, err := m.latestReleaseTag(repo)
	if err != nil {
		return err
	}

	arch, err := releaseArch(runtime.GOARCH)
	if err != nil {
		return err
	}
	asset := fmt.Sprintf("hubfly-tool-manager_linux_%s.tar.gz", arch)
	baseURL := fmt.Sprintf("https://github.com/%s/releases/download/%s", repo, tag)

	tmpDir, err := os.MkdirTemp("", "htm-self-update-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	assetPath := filepath.Join(tmpDir, asset)
	checksumsPath := filepath.Join(tmpDir, "checksums.txt")
	if err := downloadFile(baseURL+"/"+asset, assetPath); err != nil {
		return err
	}
	if err := downloadFile(baseURL+"/checksums.txt", checksumsPath); err != nil {
		return err
	}
	if err := verifyTarChecksum(assetPath, asset, checksumsPath); err != nil {
		return err
	}

	if _, err := m.runner.Run("tar", "-C", "/hubfly-tool-manager", "-xzf", assetPath); err != nil {
		return fmt.Errorf("extract release archive: %w", err)
	}
	_ = os.Chmod("/hubfly-tool-manager/bin/hubfly-tool-manager", 0o755)
	_ = os.Chmod("/hubfly-tool-manager/bin/htm", 0o755)

	if os.Geteuid() == 0 {
		_ = os.Symlink("/hubfly-tool-manager/bin/htm", "/usr/local/bin/htm")
		_ = os.Symlink("/hubfly-tool-manager/bin/hubfly-tool-manager", "/usr/local/bin/hubfly-tool-manager")
		_, _ = m.runner.Run("ln", "-sf", "/hubfly-tool-manager/bin/htm", "/usr/local/bin/htm")
		_, _ = m.runner.Run("ln", "-sf", "/hubfly-tool-manager/bin/hubfly-tool-manager", "/usr/local/bin/hubfly-tool-manager")
	}

	if err := m.runSystemctlWithSudo("daemon-reload"); err != nil {
		return fmt.Errorf("self-update daemon-reload failed: %w", err)
	}
	if err := m.runSystemctlWithSudo("restart", "hubfly-tool-manager"); err != nil {
		return fmt.Errorf("self-update restart failed: %w", err)
	}
	return nil
}

func (m *Manager) runSystemctlWithSudo(args ...string) error {
	if _, err := m.runner.Run("systemctl", args...); err == nil {
		return nil
	}
	sudoArgs := append([]string{"-n", "systemctl"}, args...)
	if _, err := m.runner.Run("sudo", sudoArgs...); err == nil {
		return nil
	}
	sudoArgs = append([]string{"systemctl"}, args...)
	if _, err := m.runner.Run("sudo", sudoArgs...); err == nil {
		return nil
	}
	return fmt.Errorf("failed to run systemctl %v (direct and sudo attempts failed)", args)
}

func (m *Manager) History(name string, limit int) ([]model.VersionRecord, error) {
	if _, err := m.mustTool(name); err != nil {
		return nil, err
	}
	return m.store.ListVersions(name, limit)
}

func (m *Manager) ListBackups(name string) ([]model.BackupSnapshot, error) {
	t, err := m.mustTool(name)
	if err != nil {
		return nil, err
	}
	return m.listBackups(t)
}

func (m *Manager) Rollback(name, backupID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	t, err := m.mustTool(name)
	if err != nil {
		return err
	}
	backups, err := m.listBackups(t)
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
	if err := m.trimBackups(t.Slug, backupRetention); err != nil {
		m.logger.Printf("backup trim warning for %s: %v", t.Name, err)
	}
	version, _ := m.GetToolVersion(t)
	_ = m.store.InsertVersion(model.VersionRecord{
		ToolName:  t.Name,
		Version:   versionOrUnknown(version),
		UpdatedAt: time.Now().UTC(),
		Notes:     fmt.Sprintf("rollback_from=%s safeguard=%s", selected.Path, safeguardDir),
	})
	return nil
}

func (m *Manager) GetToolVersion(t model.ToolConfig) (string, error) {
	if len(t.VersionCommand) == 0 {
		return "unknown", nil
	}
	cmd := make([]string, len(t.VersionCommand))
	copy(cmd, t.VersionCommand)
	for i := range cmd {
		cmd[i] = strings.ReplaceAll(cmd[i], "{binary}", t.BinaryPath)
		cmd[i] = strings.ReplaceAll(cmd[i], "{tool_dir}", t.ToolDir)
	}
	res, err := m.runner.Run(cmd[0], cmd[1:]...)
	if err != nil || res.Stdout == "" {
		return "unknown", nil
	}
	return firstLine(res.Stdout), nil
}

func (m *Manager) mustTool(name string) (model.ToolConfig, error) {
	t, err := m.store.GetTool(name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.ToolConfig{}, fmt.Errorf("unknown tool: %s", name)
		}
		return model.ToolConfig{}, err
	}
	t, err = m.normalizeToolPaths(t)
	if err != nil {
		return model.ToolConfig{}, err
	}
	return t, nil
}

func (m *Manager) normalizeToolPaths(t model.ToolConfig) (model.ToolConfig, error) {
	expectedDir := filepath.Join(m.cfg.ToolsDir, t.Slug)
	binName := binaryNameFromURL(t.DownloadURL, t.Slug)
	expectedBinary := filepath.Join(expectedDir, binName)

	currentDir := filepath.Clean(strings.TrimSpace(t.ToolDir))
	currentBinary := filepath.Clean(strings.TrimSpace(t.BinaryPath))
	if currentDir == filepath.Clean(expectedDir) && currentBinary == filepath.Clean(expectedBinary) {
		return t, nil
	}

	if err := os.MkdirAll(expectedDir, 0o755); err != nil {
		return t, fmt.Errorf("normalize tool dir for %s: %w", t.Name, err)
	}

	// Best-effort migration of an existing binary from old path to canonical path.
	if currentBinary != "" && currentBinary != "." && currentBinary != expectedBinary &&
		!isArchivePath(currentBinary) {
		if _, err := os.Stat(expectedBinary); errors.Is(err, os.ErrNotExist) {
			if _, oldErr := os.Stat(currentBinary); oldErr == nil {
				if cpErr := copyPath(currentBinary, expectedBinary); cpErr != nil {
					m.logger.Printf("warning: copy old binary path for %s failed (%s -> %s): %v", t.Name, currentBinary, expectedBinary, cpErr)
				}
			}
		}
	}

	t.ToolDir = expectedDir
	t.BinaryPath = expectedBinary
	if err := m.store.UpdateTool(t); err != nil {
		return t, fmt.Errorf("persist normalized tool paths for %s: %w", t.Name, err)
	}
	return t, nil
}

func isArchivePath(path string) bool {
	p := strings.ToLower(strings.TrimSpace(path))
	if u, err := url.Parse(p); err == nil && u.Path != "" {
		p = strings.ToLower(strings.TrimSpace(u.Path))
	}
	return strings.HasSuffix(p, ".zip") || strings.HasSuffix(p, ".tar.gz") || strings.HasSuffix(p, ".tgz")
}

func (m *Manager) ensureBinary(t model.ToolConfig) error {
	if err := os.MkdirAll(t.ToolDir, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(t.BinaryPath); err == nil {
		return os.Chmod(t.BinaryPath, 0o755)
	}
	return m.downloadBinary(t.DownloadURL, t.BinaryPath, t.Checksum)
}

func (m *Manager) runRawInDir(workDir string, cmd []string) (app.Result, error) {
	if len(cmd) == 0 {
		return app.Result{}, nil
	}
	if len(cmd[0]) == 0 {
		return app.Result{}, errors.New("command executable is empty")
	}
	if cmd[0] == "git" {
		return m.runner.RunInDir(workDir, m.cfg.GitBin, cmd[1:]...)
	}
	return m.runner.RunInDir(workDir, cmd[0], cmd[1:]...)
}

func (m *Manager) backupToolFiles(t model.ToolConfig) (string, error) {
	now := time.Now().UTC().Format("20060102T150405Z")
	dir := filepath.Join(m.cfg.BackupsDir, t.Slug, now)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}

	copyIfExists := func(src, dstName string) error {
		if _, err := os.Stat(src); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		return copyPath(src, filepath.Join(dir, dstName))
	}

	if err := copyIfExists(t.BinaryPath, "binary"); err != nil {
		return "", fmt.Errorf("backup binary: %w", err)
	}
	if err := copyIfExists(filepath.Join(t.ToolDir, "config.json"), "config.json"); err != nil {
		return "", fmt.Errorf("backup config file: %w", err)
	}
	if err := copyIfExists(filepath.Join(t.ToolDir, ".env"), ".env"); err != nil {
		return "", fmt.Errorf("backup env file: %w", err)
	}
	if err := copyIfExists(filepath.Join(t.ToolDir, "configs"), "configs"); err != nil {
		return "", fmt.Errorf("backup configs dir: %w", err)
	}
	return dir, nil
}

func (m *Manager) trimBackups(toolSlug string, keep int) error {
	if keep <= 0 {
		keep = 1
	}
	list, err := m.listBackupsBySlug(toolSlug)
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

func (m *Manager) listBackups(t model.ToolConfig) ([]model.BackupSnapshot, error) {
	return m.listBackupsBySlug(t.Slug)
}

func (m *Manager) listBackupsBySlug(toolSlug string) ([]model.BackupSnapshot, error) {
	root := filepath.Join(m.cfg.BackupsDir, toolSlug)
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
			ToolName:  toolSlug,
			Path:      filepath.Join(root, e.Name()),
			CreatedAt: info.ModTime().UTC(),
		})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.After(list[j].CreatedAt) })
	return list, nil
}

func (m *Manager) restoreToolFromBackup(t model.ToolConfig, backupDir string) error {
	restoreFileIfExists := func(src, dst string) error {
		if _, err := os.Stat(src); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		return copyPath(src, dst)
	}
	restoreDirIfExists := func(src, dst string) error {
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
	if err := restoreFileIfExists(filepath.Join(backupDir, "config.json"), filepath.Join(t.ToolDir, "config.json")); err != nil {
		return fmt.Errorf("restore config file: %w", err)
	}
	if err := restoreFileIfExists(filepath.Join(backupDir, ".env"), filepath.Join(t.ToolDir, ".env")); err != nil {
		return fmt.Errorf("restore env file: %w", err)
	}
	if err := restoreDirIfExists(filepath.Join(backupDir, "configs"), filepath.Join(t.ToolDir, "configs")); err != nil {
		return fmt.Errorf("restore configs dir: %w", err)
	}
	return os.Chmod(t.BinaryPath, 0o755)
}

func (m *Manager) downloadBinary(downloadURL, targetPath, checksum string) error {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("prepare binary dir: %w", err)
	}

	tmpPath := targetPath + ".download.tmp"
	var lastErr error
	for attempt := 1; attempt <= httpRetryAttempts; attempt++ {
		_ = os.RemoveAll(tmpPath)

		resp, err := outboundHTTPClient.Get(downloadURL) // #nosec G107
		if err != nil {
			lastErr = fmt.Errorf("download binary: %w", err)
			if attempt < httpRetryAttempts && shouldRetryHTTP(err, 0) {
				time.Sleep(retryWait(attempt))
				continue
			}
			break
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("download binary status %d", resp.StatusCode)
			_ = resp.Body.Close()
			if attempt < httpRetryAttempts && shouldRetryHTTP(nil, resp.StatusCode) {
				time.Sleep(retryWait(attempt))
				continue
			}
			break
		}

		f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			_ = resp.Body.Close()
			return fmt.Errorf("open temp binary: %w", err)
		}

		h := sha256.New()
		_, copyErr := io.Copy(io.MultiWriter(f, h), resp.Body)
		closeErr := f.Close()
		_ = resp.Body.Close()
		if copyErr != nil {
			lastErr = fmt.Errorf("write downloaded binary: %w", copyErr)
			if attempt < httpRetryAttempts && shouldRetryHTTP(copyErr, 0) {
				time.Sleep(retryWait(attempt))
				continue
			}
			break
		}
		if closeErr != nil {
			lastErr = fmt.Errorf("close temp binary: %w", closeErr)
			break
		}

		if checksum != "" {
			actual := hex.EncodeToString(h.Sum(nil))
			expected := normalizeChecksum(checksum)
			if actual != expected {
				_ = os.Remove(tmpPath)
				return fmt.Errorf("checksum mismatch: expected=%s got=%s", expected, actual)
			}
		}

		if err := m.materializeBinary(downloadURL, tmpPath, targetPath); err != nil {
			_ = os.Remove(tmpPath)
			lastErr = err
			break
		}
		if isArchivePath(downloadURL) {
			_ = removeArchiveArtifacts(filepath.Dir(targetPath))
		}
		return nil
	}

	if lastErr == nil {
		lastErr = errors.New("download binary failed")
	}
	return lastErr
}

func (m *Manager) materializeBinary(downloadURL, downloadedPath, targetPath string) error {
	urlLower := strings.ToLower(strings.TrimSpace(downloadURL))
	switch {
	case strings.HasSuffix(urlLower, ".zip"):
		defer os.Remove(downloadedPath)
		return extractBinaryFromZip(downloadedPath, targetPath)
	case strings.HasSuffix(urlLower, ".tar.gz"), strings.HasSuffix(urlLower, ".tgz"):
		defer os.Remove(downloadedPath)
		return extractBinaryFromTarGz(downloadedPath, targetPath)
	default:
		if err := os.Rename(downloadedPath, targetPath); err != nil {
			return fmt.Errorf("activate new binary: %w", err)
		}
		if err := os.Chmod(targetPath, 0o755); err != nil {
			return fmt.Errorf("chmod +x binary: %w", err)
		}
		return nil
	}
}

func (m *Manager) latestReleaseTag(repo string) (string, error) {
	u := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	var lastErr error
	for attempt := 1; attempt <= httpRetryAttempts; attempt++ {
		resp, err := outboundHTTPClient.Get(u) // #nosec G107
		if err != nil {
			lastErr = fmt.Errorf("fetch latest release: %w", err)
			if attempt < httpRetryAttempts && shouldRetryHTTP(err, 0) {
				time.Sleep(retryWait(attempt))
				continue
			}
			break
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("latest release status %d", resp.StatusCode)
			_ = resp.Body.Close()
			if attempt < httpRetryAttempts && shouldRetryHTTP(nil, resp.StatusCode) {
				time.Sleep(retryWait(attempt))
				continue
			}
			break
		}

		var payload struct {
			TagName string `json:"tag_name"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("decode latest release: %w", err)
			if attempt < httpRetryAttempts {
				time.Sleep(retryWait(attempt))
				continue
			}
			break
		}
		_ = resp.Body.Close()
		if strings.TrimSpace(payload.TagName) == "" {
			return "", errors.New("latest release tag is empty")
		}
		return strings.TrimSpace(payload.TagName), nil
	}

	if lastErr == nil {
		lastErr = errors.New("fetch latest release failed")
	}
	return "", lastErr
}

func releaseArch(goarch string) (string, error) {
	switch goarch {
	case "amd64":
		return "amd64", nil
	case "arm64":
		return "arm64", nil
	default:
		return "", fmt.Errorf("unsupported architecture for self-update: %s", goarch)
	}
}

func downloadFile(srcURL, dstPath string) error {
	var lastErr error
	for attempt := 1; attempt <= httpRetryAttempts; attempt++ {
		resp, err := outboundHTTPClient.Get(srcURL) // #nosec G107
		if err != nil {
			lastErr = fmt.Errorf("download file %s: %w", srcURL, err)
			if attempt < httpRetryAttempts && shouldRetryHTTP(err, 0) {
				time.Sleep(retryWait(attempt))
				continue
			}
			break
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("download file status %d for %s", resp.StatusCode, srcURL)
			_ = resp.Body.Close()
			if attempt < httpRetryAttempts && shouldRetryHTTP(nil, resp.StatusCode) {
				time.Sleep(retryWait(attempt))
				continue
			}
			break
		}
		f, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			_ = resp.Body.Close()
			return err
		}
		_, copyErr := io.Copy(f, resp.Body)
		closeErr := f.Close()
		_ = resp.Body.Close()
		if copyErr != nil {
			lastErr = copyErr
			if attempt < httpRetryAttempts && shouldRetryHTTP(copyErr, 0) {
				time.Sleep(retryWait(attempt))
				continue
			}
			break
		}
		if closeErr != nil {
			lastErr = closeErr
			break
		}
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("download file failed for %s", srcURL)
	}
	return lastErr
}

func shouldRetryHTTP(err error, status int) bool {
	if status == http.StatusTooManyRequests || (status >= 500 && status <= 599) {
		return true
	}
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "tls handshake timeout") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "temporary failure") ||
		strings.Contains(msg, "eof")
}

func retryWait(attempt int) time.Duration {
	wait := time.Duration(attempt) * httpRetryBaseWait
	if wait > 10*time.Second {
		return 10 * time.Second
	}
	return wait
}

func verifyTarChecksum(assetPath, assetName, checksumsPath string) error {
	data, err := os.ReadFile(checksumsPath)
	if err != nil {
		return err
	}
	expected := ""
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 && parts[len(parts)-1] == assetName {
			expected = parts[0]
			break
		}
	}
	if expected == "" {
		return fmt.Errorf("checksum not found for %s", assetName)
	}
	f, err := os.Open(assetPath)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if actual != expected {
		return fmt.Errorf("self-update checksum mismatch: expected=%s got=%s", expected, actual)
	}
	return nil
}

func normalizeChecksum(v string) string {
	x := strings.TrimSpace(strings.ToLower(v))
	x = strings.TrimPrefix(x, "sha256:")
	return x
}

func slugifyName(name string) string {
	n := strings.TrimSpace(strings.ToLower(name))
	n = strings.ReplaceAll(n, " ", "-")
	var b strings.Builder
	for _, r := range n {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune('-')
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "tool"
	}
	return out
}

func binaryNameFromURL(downloadURL, fallback string) string {
	if isArchivePath(downloadURL) {
		return fallback
	}
	u, err := url.Parse(downloadURL)
	if err != nil {
		return fallback
	}
	base := filepath.Base(strings.TrimSpace(u.Path))
	if base == "" || base == "." || base == "/" {
		return fallback
	}
	switch strings.ToLower(base) {
	case "download", "latest", "release", "releases":
		return fallback
	}
	return base
}

func cleanArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		a = strings.TrimSpace(a)
		if a != "" {
			out = append(out, a)
		}
	}
	return out
}

func versionOrUnknown(v string) string {
	if strings.TrimSpace(v) == "" {
		return "unknown"
	}
	return v
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

func extractBinaryFromZip(zipPath, dstPath string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open zip archive: %w", err)
	}
	defer r.Close()

	rootDir := filepath.Dir(dstPath)
	extractedFiles := make([]string, 0, len(r.File))
	for _, f := range r.File {
		if f == nil {
			continue
		}
		rel, ok := sanitizeArchiveEntryPath(f.Name)
		if !ok {
			continue
		}
		target := filepath.Join(rootDir, rel)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("create dir from zip %s: %w", f.Name, err)
			}
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open zip member %s: %w", f.Name, err)
		}
		mode := f.Mode()
		if mode == 0 {
			mode = 0o644
		}
		if err := writeFileFromReader(rc, target, mode); err != nil {
			_ = rc.Close()
			return fmt.Errorf("extract zip member %s: %w", f.Name, err)
		}
		_ = rc.Close()
		extractedFiles = append(extractedFiles, target)
	}

	return ensureBinaryFromExtractedFiles(extractedFiles, dstPath)
}

func extractBinaryFromTarGz(tarGzPath, dstPath string) error {
	f, err := os.Open(tarGzPath)
	if err != nil {
		return fmt.Errorf("open tar.gz archive: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("open gzip stream: %w", err)
	}
	defer gz.Close()

	rootDir := filepath.Dir(dstPath)
	extractedFiles := make([]string, 0, 16)

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar archive: %w", err)
		}
		if hdr == nil {
			continue
		}
		rel, ok := sanitizeArchiveEntryPath(hdr.Name)
		if !ok {
			continue
		}
		target := filepath.Join(rootDir, rel)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("create dir from tar %s: %w", hdr.Name, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			mode := hdr.FileInfo().Mode()
			if mode == 0 {
				mode = 0o644
			}
			if err := writeFileFromReader(tr, target, mode); err != nil {
				return fmt.Errorf("extract tar member %s: %w", hdr.Name, err)
			}
			extractedFiles = append(extractedFiles, target)
		default:
			// Ignore links and unsupported entry types for safety and portability.
			continue
		}
	}

	return ensureBinaryFromExtractedFiles(extractedFiles, dstPath)
}

func ensureBinaryFromExtractedFiles(extractedFiles []string, dstPath string) error {
	if _, err := os.Stat(dstPath); err == nil {
		return os.Chmod(dstPath, 0o755)
	}

	bestPath := ""
	bestScore := -1
	preferred := strings.ToLower(filepath.Base(dstPath))
	for _, p := range extractedFiles {
		info, err := os.Stat(p)
		if err != nil || info.IsDir() {
			continue
		}
		base := filepath.Base(p)
		if !isLikelyExecutableArchiveEntry(base) {
			continue
		}
		score := scoreArchiveEntry(base, info.Mode())
		if strings.ToLower(base) == preferred {
			score += 1000
		}
		if score > bestScore {
			bestScore = score
			bestPath = p
		}
	}
	if bestPath == "" {
		return errors.New("archive does not contain a runnable binary file")
	}
	info, err := os.Stat(bestPath)
	if err != nil {
		return err
	}
	if err := copyFile(bestPath, dstPath, info.Mode()); err != nil {
		return err
	}
	return os.Chmod(dstPath, 0o755)
}

func sanitizeArchiveEntryPath(raw string) (string, bool) {
	clean := filepath.Clean(strings.TrimSpace(raw))
	if clean == "" || clean == "." || clean == string(filepath.Separator) {
		return "", false
	}
	if filepath.IsAbs(clean) {
		return "", false
	}
	if strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", false
	}
	return clean, true
}

func isLikelyExecutableArchiveEntry(base string) bool {
	if strings.TrimSpace(base) == "" {
		return false
	}
	l := strings.ToLower(base)
	switch {
	case strings.HasSuffix(l, ".txt"),
		strings.HasSuffix(l, ".md"),
		strings.HasSuffix(l, ".json"),
		strings.HasSuffix(l, ".yaml"),
		strings.HasSuffix(l, ".yml"),
		strings.HasSuffix(l, ".toml"),
		strings.HasSuffix(l, ".env"),
		strings.HasSuffix(l, ".ini"),
		strings.HasSuffix(l, ".cfg"),
		strings.HasSuffix(l, ".conf"),
		strings.HasSuffix(l, ".service"),
		strings.HasSuffix(l, ".sh"),
		strings.HasSuffix(l, ".ps1"),
		strings.HasSuffix(l, ".zip"),
		strings.HasSuffix(l, ".tar"),
		strings.HasSuffix(l, ".tar.gz"),
		strings.HasSuffix(l, ".tgz"):
		return false
	default:
		return true
	}
}

func scoreArchiveEntry(base string, mode fs.FileMode) int {
	score := 0
	if mode&0o111 != 0 {
		score += 100
	}
	if !strings.Contains(strings.ToLower(base), "readme") {
		score += 10
	}
	return score
}

func writeFileFromReader(r io.Reader, dstPath string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return err
	}
	tmp := dstPath + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dstPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func removeArchiveArtifacts(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		if strings.HasSuffix(name, ".zip") || strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".tgz") || strings.HasSuffix(name, ".download.tmp") {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	return nil
}
