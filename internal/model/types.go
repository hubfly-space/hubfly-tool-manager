package model

import "time"

// ManagerConfig is loaded from JSON and defines all tools managed by the service.
type ManagerConfig struct {
	Manager ManagerRuntime `json:"manager"`
	Tools   []ToolConfig   `json:"tools"`
}

type ManagerRuntime struct {
	ListenAddr         string `json:"listen_addr"`
	DataDir            string `json:"data_dir"`
	BackupsDir         string `json:"backups_dir"`
	PM2Bin             string `json:"pm2_bin"`
	GitBin             string `json:"git_bin"`
	RestartOnBoot      bool   `json:"restart_on_boot"`
	CommandTimeoutSecs int    `json:"command_timeout_secs"`
}

type ToolConfig struct {
	Name           string   `json:"name"`
	Repo           string   `json:"repo"`
	Branch         string   `json:"branch"`
	WorkDir        string   `json:"work_dir"`
	BinaryPath     string   `json:"binary_path"`
	Args           []string `json:"args"`
	VersionCommand []string `json:"version_command"`
	InstallCommand []string `json:"install_command"`
	UpdateCommand  []string `json:"update_command"`
	EnvFile        string   `json:"env_file"`
	ConfigFile     string   `json:"config_file"`
	ConfigsDir     string   `json:"configs_dir"`
	Enabled        bool     `json:"enabled"`
}

type ToolRuntimeStatus struct {
	Name      string    `json:"name"`
	PM2Status string    `json:"pm2_status"`
	Version   string    `json:"version"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
	Error     string    `json:"error,omitempty"`
}

type VersionRecord struct {
	ID         int64     `json:"id"`
	ToolName   string    `json:"tool_name"`
	Version    string    `json:"version"`
	UpdatedAt  time.Time `json:"updated_at"`
	CommitHash string    `json:"commit_hash,omitempty"`
	Notes      string    `json:"notes,omitempty"`
}
