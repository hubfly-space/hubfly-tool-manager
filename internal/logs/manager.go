package logs

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"hubfly-tool-manager/internal/model"
)

const (
	defaultMaxFileSize = 5 * 1024 * 1024
	defaultMaxArchives = 8
	defaultSearchLimit = 200
)

type Manager struct {
	rootDir      string
	maxFileSize  int64
	maxArchives  int
	mu           sync.Mutex
}

type Paths struct {
	Dir     string
	BootLog string
	Stdout  string
	Stderr  string
}

func New(rootDir string) *Manager {
	return &Manager{
		rootDir:     filepath.Clean(rootDir),
		maxFileSize: defaultMaxFileSize,
		maxArchives: defaultMaxArchives,
	}
}

func (m *Manager) Paths(toolSlug string) Paths {
	dir := filepath.Join(m.rootDir, toolSlug)
	return Paths{
		Dir:     dir,
		BootLog: filepath.Join(dir, "boot.log"),
		Stdout:  filepath.Join(dir, "stdout.log"),
		Stderr:  filepath.Join(dir, "stderr.log"),
	}
}

func (m *Manager) Ensure(toolSlug string) (Paths, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	paths := m.Paths(toolSlug)
	if err := os.MkdirAll(paths.Dir, 0o755); err != nil {
		return Paths{}, fmt.Errorf("create log dir: %w", err)
	}
	for _, p := range []string{paths.BootLog, paths.Stdout, paths.Stderr} {
		if err := ensureFile(p); err != nil {
			return Paths{}, err
		}
	}
	return paths, nil
}

func (m *Manager) AppendBoot(toolSlug, message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	paths := m.Paths(toolSlug)
	if err := os.MkdirAll(paths.Dir, 0o755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}
	if err := m.rotateIfNeededLocked(paths.BootLog); err != nil {
		return err
	}
	f, err := os.OpenFile(paths.BootLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open boot log: %w", err)
	}
	defer f.Close()
	line := fmt.Sprintf("%s %s\n", time.Now().UTC().Format(time.RFC3339Nano), strings.TrimSpace(message))
	if _, err := f.WriteString(line); err != nil {
		return fmt.Errorf("write boot log: %w", err)
	}
	return nil
}

func (m *Manager) Rotate(toolSlug string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	paths := m.Paths(toolSlug)
	if err := os.MkdirAll(paths.Dir, 0o755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}
	for _, p := range []string{paths.BootLog, paths.Stdout, paths.Stderr} {
		if err := m.rotateIfNeededLocked(p); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) Summarize(toolSlug string) model.ToolLogSummary {
	m.mu.Lock()
	defer m.mu.Unlock()

	paths := m.Paths(toolSlug)
	summary := model.ToolLogSummary{
		Dir:         paths.Dir,
		BootLogPath: paths.BootLog,
		StdoutPath:  paths.Stdout,
		StderrPath:  paths.Stderr,
	}
	entries, err := os.ReadDir(paths.Dir)
	if err != nil {
		return summary
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		summary.FileCount++
		summary.TotalBytes += info.Size()
		if info.ModTime().After(summary.LastWriteAt) {
			summary.LastWriteAt = info.ModTime().UTC()
		}
	}
	return summary
}

func (m *Manager) Search(toolName, toolSlug, fileName, query string, limit int) ([]model.LogQueryResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if limit <= 0 || limit > 1000 {
		limit = defaultSearchLimit
	}
	paths := m.Paths(toolSlug)
	files, err := m.listFilesLocked(paths.Dir, fileName)
	if err != nil {
		return nil, err
	}

	query = strings.ToLower(strings.TrimSpace(query))
	results := make([]model.LogQueryResult, 0, limit)
	for _, path := range files {
		lines, err := tailLines(path, 4000)
		if err != nil {
			continue
		}
		lineNoStart := 1
		if len(lines) > 0 {
			lineNoStart = 1
		}
		for idx, line := range lines {
			if query != "" && !strings.Contains(strings.ToLower(line), query) {
				continue
			}
			results = append(results, model.LogQueryResult{
				Tool:       toolName,
				File:       filepath.Base(path),
				LineNumber: lineNoStart + idx,
				Timestamp:  parseLeadingTime(line),
				Text:       line,
			})
			if len(results) >= limit {
				return results, nil
			}
		}
	}
	return results, nil
}

func (m *Manager) Cleanup(toolSlug, fileName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	paths := m.Paths(toolSlug)
	if fileName == "" {
		for _, path := range []string{paths.BootLog, paths.Stdout, paths.Stderr} {
			if err := ensureFile(path); err != nil {
				return err
			}
			if err := os.WriteFile(path, nil, 0o644); err != nil {
				return fmt.Errorf("truncate log %s: %w", path, err)
			}
		}
		files, err := filepath.Glob(filepath.Join(paths.Dir, "*.log.*"))
		if err != nil {
			return fmt.Errorf("list archived logs: %w", err)
		}
		for _, path := range files {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove archived log %s: %w", path, err)
			}
		}
		return nil
	}

	target := filepath.Join(paths.Dir, filepath.Base(fileName))
	if err := ensureFile(target); err != nil {
		return err
	}
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		return fmt.Errorf("truncate log %s: %w", target, err)
	}
	matches, err := filepath.Glob(target + ".*")
	if err != nil {
		return fmt.Errorf("list archived logs: %w", err)
	}
	for _, path := range matches {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove archived log %s: %w", path, err)
		}
	}
	return nil
}

func ensureFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}
	_, err := os.Stat(path)
	if err == nil {
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("stat log file %s: %w", path, err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		return fmt.Errorf("create log file %s: %w", path, err)
	}
	return nil
}

func (m *Manager) rotateIfNeededLocked(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ensureFile(path)
		}
		return fmt.Errorf("stat log file %s: %w", path, err)
	}
	if info.Size() < m.maxFileSize {
		return nil
	}
	archived := fmt.Sprintf("%s.%s", path, time.Now().UTC().Format("20060102T150405Z"))
	if err := os.Rename(path, archived); err != nil {
		return fmt.Errorf("rotate log %s: %w", path, err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		return fmt.Errorf("recreate log %s: %w", path, err)
	}
	matches, err := filepath.Glob(path + ".*")
	if err != nil {
		return fmt.Errorf("list rotated logs %s: %w", path, err)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(matches)))
	for idx, old := range matches {
		if idx < m.maxArchives {
			continue
		}
		_ = os.Remove(old)
	}
	return nil
}

func (m *Manager) listFilesLocked(dir, fileName string) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	var patterns []string
	if strings.TrimSpace(fileName) != "" {
		base := filepath.Base(fileName)
		patterns = []string{filepath.Join(dir, base), filepath.Join(dir, base+".*")}
	} else {
		patterns = []string{
			filepath.Join(dir, "*.log"),
			filepath.Join(dir, "*.log.*"),
		}
	}
	seen := map[string]struct{}{}
	files := make([]string, 0, 8)
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("glob logs: %w", err)
		}
		for _, match := range matches {
			if _, ok := seen[match]; ok {
				continue
			}
			seen[match] = struct{}{}
			files = append(files, match)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	return files, nil
}

func tailLines(path string, maxLines int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	lines := make([]string, 0, maxLines)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > maxLines {
			lines = lines[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

func parseLeadingTime(line string) time.Time {
	parts := strings.Fields(strings.TrimSpace(line))
	if len(parts) == 0 {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
