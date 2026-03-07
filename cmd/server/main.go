package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"hubfly-tool-manager/internal/config"
	"hubfly-tool-manager/internal/db"
	"hubfly-tool-manager/internal/httpapi"
	"hubfly-tool-manager/internal/logx"
	"hubfly-tool-manager/internal/pm2"
	"hubfly-tool-manager/internal/tool"
)

func main() {
	cfgPath := flag.String("config", "./configs/config.json", "path to config file")
	flag.Parse()

	logger := logx.New()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		logger.Fatalf("load config: %v", err)
	}

	if err := os.MkdirAll(cfg.Manager.DataDir, 0o755); err != nil {
		logger.Fatalf("create data dir: %v", err)
	}
	dbPath := filepath.Join(cfg.Manager.DataDir, "manager.sqlite")
	store, err := db.Open(dbPath)
	if err != nil {
		logger.Fatalf("open database: %v", err)
	}
	defer store.Close()

	pm2Client := pm2.New(cfg.Manager.PM2Bin, time.Duration(cfg.Manager.CommandTimeoutSecs)*time.Second, logger)
	mgr := tool.NewManager(cfg, store, pm2Client, logger)
	if err := mgr.EnsureRuntime(); err != nil {
		logger.Fatalf("runtime check failed: %v", err)
	}

	if cfg.Manager.RestartOnBoot {
		for _, t := range cfg.Tools {
			if !t.Enabled {
				continue
			}
			if err := mgr.Start(t.Name); err != nil {
				logger.Printf("boot start warning for %s: %v", t.Name, err)
			}
		}
	}

	srv := httpapi.New(mgr, logger)
	if err := httpapi.ListenAndServe(cfg.Manager.ListenAddr, srv.Handler(), logger); err != nil {
		logger.Fatalf("server error: %v", err)
	}

	fmt.Println("shutdown")
}
