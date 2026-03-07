package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"hubfly-tool-manager/internal/model"
)

func Load(path string) (model.ManagerConfig, error) {
	var cfg model.ManagerConfig

	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config json: %w", err)
	}

	applyDefaults(&cfg)
	if err := validate(cfg); err != nil {
		return cfg, err
	}

	return cfg, nil
}

func applyDefaults(cfg *model.ManagerConfig) {
	if cfg.Manager.ListenAddr == "" {
		cfg.Manager.ListenAddr = ":10000"
	}
	if cfg.Manager.DataDir == "" {
		cfg.Manager.DataDir = "./data"
	}
	if cfg.Manager.BackupsDir == "" {
		cfg.Manager.BackupsDir = "./backups"
	}
	if cfg.Manager.PM2Bin == "" {
		cfg.Manager.PM2Bin = "pm2"
	}
	if cfg.Manager.GitBin == "" {
		cfg.Manager.GitBin = "git"
	}
	if cfg.Manager.CommandTimeoutSecs <= 0 {
		cfg.Manager.CommandTimeoutSecs = 90
	}

	for i := range cfg.Tools {
		if cfg.Tools[i].Branch == "" {
			cfg.Tools[i].Branch = "main"
		}
	}
}

func validate(cfg model.ManagerConfig) error {
	if cfg.Manager.ListenAddr == "" {
		return errors.New("manager.listen_addr is required")
	}

	seen := map[string]struct{}{}
	for _, t := range cfg.Tools {
		if t.Name == "" {
			return errors.New("tool.name is required")
		}
		if _, ok := seen[t.Name]; ok {
			return fmt.Errorf("duplicate tool.name: %s", t.Name)
		}
		seen[t.Name] = struct{}{}

		if t.WorkDir == "" {
			return fmt.Errorf("tool.work_dir is required for %s", t.Name)
		}
		if !filepath.IsAbs(t.WorkDir) {
			return fmt.Errorf("tool.work_dir must be absolute for %s", t.Name)
		}
		if t.BinaryPath == "" {
			return fmt.Errorf("tool.binary_path is required for %s", t.Name)
		}
		if !filepath.IsAbs(t.BinaryPath) {
			return fmt.Errorf("tool.binary_path must be absolute for %s", t.Name)
		}
		if len(t.InstallCommand) > 0 && t.InstallCommand[0] == "" {
			return fmt.Errorf("tool.install_command executable cannot be empty for %s", t.Name)
		}
		if len(t.UpdateCommand) > 0 && t.UpdateCommand[0] == "" {
			return fmt.Errorf("tool.update_command executable cannot be empty for %s", t.Name)
		}
	}

	return nil
}
