package main

import (
	"flag"
	"os"
	"path/filepath"
	"time"

	"hubfly-tool-manager/internal/db"
	"hubfly-tool-manager/internal/httpapi"
	"hubfly-tool-manager/internal/logx"
	"hubfly-tool-manager/internal/model"
	"hubfly-tool-manager/internal/pm2"
	"hubfly-tool-manager/internal/tool"
)

func main() {
	listenAddr := flag.String("listen-addr", envOrDefault("HTM_LISTEN_ADDR", ":10000"), "http listen address")
	dataDir := flag.String("data-dir", envOrDefault("HTM_DATA_DIR", "/hubfly-tool-manager/data"), "directory for sqlite database")
	backupsDir := flag.String("backups-dir", envOrDefault("HTM_BACKUPS_DIR", "/hubfly-tool-manager/backups"), "directory for backups")
	toolsDir := flag.String("tools-dir", envOrDefault("HTM_TOOLS_DIR", "/hubfly-tool-manager/tools"), "directory holding per-tool folders")
	tokenFile := flag.String("token-file", envOrDefault("HTM_TOKEN_FILE", "/hubfly-tool-manager/.token"), "security token file path")
	lockdownFile := flag.String("lockdown-file", envOrDefault("HTM_LOCKDOWN_FILE", "/hubfly-tool-manager/.lockdown.json"), "lockdown state file path")
	pm2Bin := flag.String("pm2-bin", envOrDefault("HTM_PM2_BIN", "pm2"), "pm2 binary")
	gitBin := flag.String("git-bin", envOrDefault("HTM_GIT_BIN", "git"), "git binary")
	timeoutSecs := flag.Int("command-timeout-secs", envIntOrDefault("HTM_COMMAND_TIMEOUT_SECS", 90), "command timeout in seconds")
	restartOnBoot := flag.Bool("restart-on-boot", envBoolOrDefault("HTM_RESTART_ON_BOOT", true), "ensure registered tools are running at boot")
	flag.Parse()

	logger := logx.New()

	cfg := model.RuntimeConfig{
		ListenAddr:         *listenAddr,
		DataDir:            absPathOrDefault(*dataDir, "/hubfly-tool-manager/data"),
		BackupsDir:         absPathOrDefault(*backupsDir, "/hubfly-tool-manager/backups"),
		ToolsDir:           absPathOrDefault(*toolsDir, "/hubfly-tool-manager/tools"),
		TokenFile:          absPathOrDefault(*tokenFile, "/hubfly-tool-manager/.token"),
		LockdownFile:       absPathOrDefault(*lockdownFile, "/hubfly-tool-manager/.lockdown.json"),
		PM2Bin:             *pm2Bin,
		GitBin:             *gitBin,
		RestartOnBoot:      *restartOnBoot,
		CommandTimeoutSecs: *timeoutSecs,
	}

	for _, dir := range []string{cfg.DataDir, cfg.BackupsDir, cfg.ToolsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			logger.Fatalf("create runtime dir %s: %v", dir, err)
		}
	}

	dbPath := filepath.Join(cfg.DataDir, "manager.sqlite")
	store, err := db.Open(dbPath)
	if err != nil {
		logger.Fatalf("open database: %v", err)
	}
	defer store.Close()

	pm2Client := pm2.New(cfg.PM2Bin, time.Duration(cfg.CommandTimeoutSecs)*time.Second, logger)
	mgr := tool.NewManager(cfg, store, pm2Client, logger)
	if err := mgr.EnsureRuntime(); err != nil {
		logger.Fatalf("runtime check failed: %v", err)
	}

	if cfg.RestartOnBoot {
		mgr.EnsureAllRegisteredRunning()
	}

	srv := httpapi.New(mgr, logger, cfg.TokenFile, cfg.LockdownFile)
	if err := httpapi.ListenAndServe(cfg.ListenAddr, srv.Handler(), logger); err != nil {
		logger.Fatalf("server error: %v", err)
	}
}

func absPathOrDefault(pathValue, fallback string) string {
	v := pathValue
	if v == "" {
		v = fallback
	}
	if filepath.IsAbs(v) {
		return filepath.Clean(v)
	}
	abs, err := filepath.Abs(v)
	if err != nil {
		return filepath.Clean(fallback)
	}
	return filepath.Clean(abs)
}
