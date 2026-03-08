package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"hubfly-tool-manager/internal/model"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;`); err != nil {
		return nil, fmt.Errorf("configure sqlite pragmas: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	query := `
CREATE TABLE IF NOT EXISTS tools (
  name TEXT PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  tool_dir TEXT NOT NULL,
  binary_path TEXT NOT NULL,
  download_url TEXT NOT NULL,
  checksum TEXT,
  args_json TEXT NOT NULL,
  version_command_json TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tool_versions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  tool_name TEXT NOT NULL,
  version TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  commit_hash TEXT,
  notes TEXT,
  FOREIGN KEY(tool_name) REFERENCES tools(name) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_tool_versions_tool_name_updated_at
  ON tool_versions(tool_name, updated_at DESC);
`
	if _, err := s.db.Exec(query); err != nil {
		return fmt.Errorf("migrate sqlite: %w", err)
	}
	return nil
}

func (s *Store) CreateTool(tool model.ToolConfig) error {
	argsJSON, err := json.Marshal(tool.Args)
	if err != nil {
		return fmt.Errorf("marshal args: %w", err)
	}
	versionCmdJSON, err := json.Marshal(tool.VersionCommand)
	if err != nil {
		return fmt.Errorf("marshal version command: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)

	_, err = s.db.Exec(
		`INSERT INTO tools (name, slug, tool_dir, binary_path, download_url, checksum, args_json, version_command_json, created_at, updated_at)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tool.Name,
		tool.Slug,
		tool.ToolDir,
		tool.BinaryPath,
		tool.DownloadURL,
		tool.Checksum,
		string(argsJSON),
		string(versionCmdJSON),
		now,
		now,
	)
	if err != nil {
		return fmt.Errorf("create tool: %w", err)
	}
	return nil
}

func (s *Store) GetTool(name string) (model.ToolConfig, error) {
	row := s.db.QueryRow(
		`SELECT name, slug, tool_dir, binary_path, download_url, checksum, args_json, version_command_json, created_at, updated_at
         FROM tools WHERE name = ?`,
		name,
	)
	return scanTool(row)
}

func (s *Store) ListTools() ([]model.ToolConfig, error) {
	rows, err := s.db.Query(
		`SELECT name, slug, tool_dir, binary_path, download_url, checksum, args_json, version_command_json, created_at, updated_at
         FROM tools ORDER BY name ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	defer rows.Close()

	out := make([]model.ToolConfig, 0, 8)
	for rows.Next() {
		t, err := scanTool(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tools: %w", err)
	}
	return out, nil
}

func (s *Store) DeleteTool(name string) error {
	_, err := s.db.Exec(`DELETE FROM tools WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("delete tool: %w", err)
	}
	return nil
}

func (s *Store) UpdateTool(tool model.ToolConfig) error {
	argsJSON, err := json.Marshal(tool.Args)
	if err != nil {
		return fmt.Errorf("marshal args: %w", err)
	}
	versionCmdJSON, err := json.Marshal(tool.VersionCommand)
	if err != nil {
		return fmt.Errorf("marshal version command: %w", err)
	}

	_, err = s.db.Exec(
		`UPDATE tools
         SET binary_path = ?, download_url = ?, checksum = ?, args_json = ?, version_command_json = ?, updated_at = ?
         WHERE name = ?`,
		tool.BinaryPath,
		tool.DownloadURL,
		tool.Checksum,
		string(argsJSON),
		string(versionCmdJSON),
		time.Now().UTC().Format(time.RFC3339Nano),
		tool.Name,
	)
	if err != nil {
		return fmt.Errorf("update tool: %w", err)
	}
	return nil
}

func (s *Store) DeleteVersions(name string) error {
	_, err := s.db.Exec(`DELETE FROM tool_versions WHERE tool_name = ?`, name)
	if err != nil {
		return fmt.Errorf("delete versions: %w", err)
	}
	return nil
}

func (s *Store) InsertVersion(record model.VersionRecord) error {
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = time.Now().UTC()
	}
	_, err := s.db.Exec(
		`INSERT INTO tool_versions (tool_name, version, updated_at, commit_hash, notes)
         VALUES (?, ?, ?, ?, ?)`,
		record.ToolName,
		record.Version,
		record.UpdatedAt.Format(time.RFC3339Nano),
		record.CommitHash,
		record.Notes,
	)
	if err != nil {
		return fmt.Errorf("insert version record: %w", err)
	}
	return nil
}

func (s *Store) LatestVersion(toolName string) (model.VersionRecord, error) {
	var r model.VersionRecord
	var ts string
	err := s.db.QueryRow(
		`SELECT id, tool_name, version, updated_at, commit_hash, notes
         FROM tool_versions
         WHERE tool_name = ?
         ORDER BY updated_at DESC
         LIMIT 1`,
		toolName,
	).Scan(&r.ID, &r.ToolName, &r.Version, &ts, &r.CommitHash, &r.Notes)
	if err != nil {
		return r, err
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return r, fmt.Errorf("parse updated_at: %w", err)
	}
	r.UpdatedAt = t
	return r, nil
}

func (s *Store) ListVersions(toolName string, limit int) ([]model.VersionRecord, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(
		`SELECT id, tool_name, version, updated_at, commit_hash, notes
         FROM tool_versions
         WHERE tool_name = ?
         ORDER BY updated_at DESC
         LIMIT ?`,
		toolName,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list versions: %w", err)
	}
	defer rows.Close()

	out := make([]model.VersionRecord, 0, limit)
	for rows.Next() {
		var r model.VersionRecord
		var ts string
		if err := rows.Scan(&r.ID, &r.ToolName, &r.Version, &ts, &r.CommitHash, &r.Notes); err != nil {
			return nil, fmt.Errorf("scan version row: %w", err)
		}
		t, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return nil, fmt.Errorf("parse updated_at: %w", err)
		}
		r.UpdatedAt = t
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate versions: %w", err)
	}
	return out, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTool(row rowScanner) (model.ToolConfig, error) {
	var t model.ToolConfig
	var argsJSON, versionCmdJSON, createdAt, updatedAt string
	err := row.Scan(
		&t.Name,
		&t.Slug,
		&t.ToolDir,
		&t.BinaryPath,
		&t.DownloadURL,
		&t.Checksum,
		&argsJSON,
		&versionCmdJSON,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return t, sql.ErrNoRows
		}
		return t, fmt.Errorf("scan tool: %w", err)
	}
	if err := json.Unmarshal([]byte(argsJSON), &t.Args); err != nil {
		return t, fmt.Errorf("unmarshal args: %w", err)
	}
	if err := json.Unmarshal([]byte(versionCmdJSON), &t.VersionCommand); err != nil {
		return t, fmt.Errorf("unmarshal version command: %w", err)
	}
	ct, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return t, fmt.Errorf("parse created_at: %w", err)
	}
	ut, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return t, fmt.Errorf("parse updated_at: %w", err)
	}
	t.CreatedAt = ct
	t.UpdatedAt = ut
	return t, nil
}
