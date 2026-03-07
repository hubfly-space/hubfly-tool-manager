package db

import (
	"database/sql"
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
CREATE TABLE IF NOT EXISTS tool_versions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  tool_name TEXT NOT NULL,
  version TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  commit_hash TEXT,
  notes TEXT
);
CREATE INDEX IF NOT EXISTS idx_tool_versions_tool_name_updated_at
  ON tool_versions(tool_name, updated_at DESC);
`
	if _, err := s.db.Exec(query); err != nil {
		return fmt.Errorf("migrate sqlite: %w", err)
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
