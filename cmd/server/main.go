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
	restartOnBoot := flag.Bool("restart-on-boot", envBoolOrDefault("HTM_RESTART_ON_BOOT", false), "restart all registered tools at boot")
	flag.Parse()

	logger := logx.New()

	cfg := model.RuntimeConfig{
		ListenAddr:         *listenAddr,
		DataDir:            *dataDir,
		BackupsDir:         *backupsDir,
		ToolsDir:           *toolsDir,
		TokenFile:          *tokenFile,
		LockdownFile:       *lockdownFile,
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
		mgr.StartAllRegistered()
	}

	srv := httpapi.New(mgr, logger, cfg.TokenFile, cfg.LockdownFile)
	if err := httpapi.ListenAndServe(cfg.ListenAddr, srv.Handler(), logger); err != nil {
		logger.Fatalf("server error: %v", err)
	}
}
