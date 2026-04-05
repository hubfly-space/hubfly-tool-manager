package model

import "time"

type RuntimeConfig struct {
	ListenAddr         string
	DataDir            string
	BackupsDir         string
	ToolsDir           string
	LogsDir            string
	TokenFile          string
	LockdownFile       string
	SessionSecretFile  string
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
	Name           string             `json:"name"`
	Slug           string             `json:"slug,omitempty"`
	PM2Status      string             `json:"pm2_status"`
	Version        string             `json:"version"`
	UpdatedAt      time.Time          `json:"updated_at,omitempty"`
	Error          string             `json:"error,omitempty"`
	DownloadURL    string             `json:"download_url,omitempty"`
	Checksum       string             `json:"checksum,omitempty"`
	Args           []string           `json:"args,omitempty"`
	VersionCommand []string           `json:"version_command,omitempty"`
	CreatedAt      time.Time          `json:"created_at,omitempty"`
	DBUpdatedAt    time.Time          `json:"db_updated_at,omitempty"`
	ToolDir        string             `json:"tool_dir,omitempty"`
	BinaryPath     string             `json:"binary_path,omitempty"`
	Logs           ToolLogSummary     `json:"logs"`
	Release        *ReleaseSuggestion `json:"release,omitempty"`
}

type ToolLogSummary struct {
	Dir         string    `json:"dir,omitempty"`
	BootLogPath string    `json:"boot_log_path,omitempty"`
	StdoutPath  string    `json:"stdout_path,omitempty"`
	StderrPath  string    `json:"stderr_path,omitempty"`
	FileCount   int       `json:"file_count"`
	TotalBytes  int64     `json:"total_bytes"`
	LastWriteAt time.Time `json:"last_write_at,omitempty"`
}

type ReleaseSuggestion struct {
	Supported            bool   `json:"supported"`
	Repository           string `json:"repository,omitempty"`
	CurrentTag           string `json:"current_tag,omitempty"`
	LatestTag            string `json:"latest_tag,omitempty"`
	CurrentAsset         string `json:"current_asset,omitempty"`
	LatestAsset          string `json:"latest_asset,omitempty"`
	SuggestedDownloadURL string `json:"suggested_download_url,omitempty"`
	UpdateAvailable      bool   `json:"update_available"`
	Reason               string `json:"reason,omitempty"`
	Error                string `json:"error,omitempty"`
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

type BulkActionResult struct {
	Tool    string `json:"tool"`
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

type LogQueryResult struct {
	Tool       string    `json:"tool"`
	File       string    `json:"file"`
	LineNumber int       `json:"line_number"`
	Timestamp  time.Time `json:"timestamp,omitempty"`
	Text       string    `json:"text"`
}
