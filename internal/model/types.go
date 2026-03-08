package model

import "time"

type RuntimeConfig struct {
	ListenAddr         string
	DataDir            string
	BackupsDir         string
	ToolsDir           string
	TokenFile          string
	PM2Bin             string
	GitBin             string
	RestartOnBoot      bool
	CommandTimeoutSecs int
}

type ToolConfig struct {
	Name           string    `json:"name"`
	Slug           string    `json:"slug"`
	ToolDir        string    `json:"tool_dir"`
	BinaryPath     string    `json:"binary_path"`
	DownloadURL    string    `json:"download_url"`
	Checksum       string    `json:"checksum,omitempty"`
	Args           []string  `json:"args,omitempty"`
	VersionCommand []string  `json:"version_command,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type RegisterToolRequest struct {
	Name           string   `json:"name"`
	DownloadURL    string   `json:"download_url"`
	Checksum       string   `json:"checksum,omitempty"`
	Args           []string `json:"args,omitempty"`
	VersionCommand []string `json:"version_command,omitempty"`
}

type ConfigureToolRequest struct {
	DownloadURL    *string   `json:"download_url,omitempty"`
	Checksum       *string   `json:"checksum,omitempty"`
	Args           *[]string `json:"args,omitempty"`
	VersionCommand *[]string `json:"version_command,omitempty"`
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

type BackupSnapshot struct {
	ID        string    `json:"id"`
	ToolName  string    `json:"tool_name"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"`
}
