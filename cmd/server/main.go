package main

import (
	"flag"
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
	dataDir := flag.String("data-dir", envOrDefault("HTM_DATA_DIR", "./data"), "directory for sqlite database")
	backupsDir := flag.String("backups-dir", envOrDefault("HTM_BACKUPS_DIR", "./backups"), "directory for backups")
	toolsDir := flag.String("tools-dir", envOrDefault("HTM_TOOLS_DIR", "./tools"), "directory holding per-tool folders")
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
		PM2Bin:             *pm2Bin,
		GitBin:             *gitBin,
		RestartOnBoot:      *restartOnBoot,
		CommandTimeoutSecs: *timeoutSecs,
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

	srv := httpapi.New(mgr, logger)
	if err := httpapi.ListenAndServe(cfg.ListenAddr, srv.Handler(), logger); err != nil {
		logger.Fatalf("server error: %v", err)
	}
}
