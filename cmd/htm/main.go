package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	baseURL := os.Getenv("HTM_SERVER")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:10000"
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "init":
		err = initToken(args)
	case "unlock":
		err = unlockLockdown(args)
	case "manager-version":
		err = doGet(baseURL, "/version", false)
	case "health":
		err = doGet(baseURL, "/health", true)
	case "register":
		err = register(baseURL, args)
	case "list":
		err = doGet(baseURL, "/tools", true)
	case "status":
		err = requireToolAndGet(baseURL, args, "")
	case "version":
		err = requireToolAndGet(baseURL, args, "/version")
	case "history":
		err = history(baseURL, args)
	case "backups":
		err = requireToolAndGet(baseURL, args, "/backups")
	case "start", "stop", "restart", "provision", "update":
		err = requireToolAndPost(baseURL, args, "/"+cmd, nil)
	case "configure-update":
		err = configureUpdate(baseURL, args)
	case "rollback":
		err = rollback(baseURL, args)
	case "cleanup":
		err = requireToolAndPost(baseURL, args, "/cleanup", nil)
	case "self-update":
		err = selfUpdate(baseURL, args)
	default:
		usage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`Hubfly Tool Manager CLI

Usage:
  htm health
  htm manager-version
  htm init [TOKEN]
  htm unlock
  htm register --name <name> --url <download_url> [--checksum <sha256>] [--version-cmd <comma-separated>] [--args <comma-separated>]
  htm list
  htm status <tool>
  htm version <tool>
  htm history <tool> [limit]
  htm backups <tool>
  htm start <tool>
  htm stop <tool>
  htm restart <tool>
  htm provision <tool>
  htm update <tool>
  htm configure-update <tool> [--url <download_url>] [--checksum <sha256-or-empty>] [--version-cmd <comma-separated>] [--args <comma-separated>]
  htm rollback <tool> [backup_id]
  htm cleanup <tool>
  htm self-update

Env:
  HTM_SERVER     default: http://127.0.0.1:10000
  HTM_TOKEN_FILE default: /hubfly-tool-manager/.token
  HTM_LOCKDOWN_FILE default: /hubfly-tool-manager/.lockdown.json`)
}

func register(baseURL string, args []string) error {
	fs := flag.NewFlagSet("register", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("name", "", "tool name")
	downloadURL := fs.String("url", "", "download url")
	checksum := fs.String("checksum", "", "optional sha256 checksum")
	versionCmd := fs.String("version-cmd", "", "comma-separated version command")
	toolArgs := fs.String("args", "", "comma-separated runtime args")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*name) == "" {
		return fmt.Errorf("--name is required")
	}
	if strings.TrimSpace(*downloadURL) == "" {
		return fmt.Errorf("--url is required")
	}

	body := map[string]any{
		"name":         strings.TrimSpace(*name),
		"download_url": strings.TrimSpace(*downloadURL),
	}
	if strings.TrimSpace(*checksum) != "" {
		body["checksum"] = strings.TrimSpace(*checksum)
	}
	if items := parseCSV(*versionCmd); len(items) > 0 {
		body["version_command"] = items
	}
	if items := parseCSV(*toolArgs); len(items) > 0 {
		body["args"] = items
	}
	return doPost(baseURL, "/tools/register", body, true)
}

func parseCSV(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func history(baseURL string, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("missing tool name")
	}
	path := "/tools/" + url.PathEscape(args[0]) + "/history"
	if len(args) > 1 {
		limit, err := strconv.Atoi(args[1])
		if err != nil || limit <= 0 {
			return fmt.Errorf("invalid limit")
		}
		path += "?limit=" + strconv.Itoa(limit)
	}
	return doGet(baseURL, path, true)
}

func selfUpdate(baseURL string, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("self-update does not take arguments")
	}
	return doPost(baseURL, "/self/update", map[string]any{}, true)
}

func configureUpdate(baseURL string, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("missing tool name")
	}
	toolName := args[0]
	fs := flag.NewFlagSet("configure-update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	downloadURL := fs.String("url", "", "download url")
	checksum := fs.String("checksum", "", "checksum (can be empty to clear)")
	versionCmd := fs.String("version-cmd", "", "comma-separated version command")
	toolArgs := fs.String("args", "", "comma-separated runtime args")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	body := map[string]any{}
	if fs.Lookup("url").Value.String() != "" {
		body["download_url"] = strings.TrimSpace(*downloadURL)
	}
	if fs.Lookup("checksum").Value.String() != "" || strings.Contains(strings.Join(args[1:], " "), "--checksum") {
		body["checksum"] = strings.TrimSpace(*checksum)
	}
	if fs.Lookup("version-cmd").Value.String() != "" {
		body["version_command"] = parseCSV(*versionCmd)
	}
	if fs.Lookup("args").Value.String() != "" {
		body["args"] = parseCSV(*toolArgs)
	}
	if len(body) == 0 {
		return fmt.Errorf("provide at least one change (--url, --checksum, --version-cmd, --args)")
	}

	return doPost(baseURL, "/tools/"+url.PathEscape(toolName)+"/configure-update", body, true)
}

func rollback(baseURL string, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("missing tool name")
	}
	body := map[string]any{}
	if len(args) > 1 {
		body["backup_id"] = args[1]
	}
	return doPost(baseURL, "/tools/"+url.PathEscape(args[0])+"/rollback", body, true)
}

func requireToolAndGet(baseURL string, args []string, suffix string) error {
	if len(args) < 1 {
		return fmt.Errorf("missing tool name")
	}
	path := "/tools/" + url.PathEscape(args[0]) + suffix
	return doGet(baseURL, path, true)
}

func requireToolAndPost(baseURL string, args []string, suffix string, body any) error {
	if len(args) < 1 {
		return fmt.Errorf("missing tool name")
	}
	path := "/tools/" + url.PathEscape(args[0]) + suffix
	return doPost(baseURL, path, body, true)
}

func doGet(baseURL, path string, needAuth bool) error {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(baseURL, "/")+path, nil)
	if err != nil {
		return err
	}
	if needAuth {
		token, err := readToken()
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return printResponse(resp)
}

func doPost(baseURL, path string, body any, needAuth bool) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(baseURL, "/")+path, reader)
	if err != nil {
		return err
	}
	if needAuth {
		token, err := readToken()
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return printResponse(resp)
}

func printResponse(resp *http.Response) error {
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		if len(data) > 0 {
			return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(data)))
		}
		return fmt.Errorf("%s", resp.Status)
	}
	if len(data) == 0 {
		fmt.Println("{}")
		return nil
	}
	fmt.Println(string(data))
	return nil
}

func httpClient() *http.Client {
	return &http.Client{Timeout: 90 * time.Second}
}

func tokenFilePath() string {
	if p := strings.TrimSpace(os.Getenv("HTM_TOKEN_FILE")); p != "" {
		return p
	}
	return "/hubfly-tool-manager/.token"
}

func readToken() (string, error) {
	b, err := os.ReadFile(tokenFilePath())
	if err != nil {
		return "", fmt.Errorf("read token file (%s): %w; run `htm init`", tokenFilePath(), err)
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return "", fmt.Errorf("token file (%s) is empty; run `htm init`", tokenFilePath())
	}
	return token, nil
}

func initToken(args []string) error {
	var token string
	if len(args) > 0 {
		token = strings.TrimSpace(args[0])
	} else {
		fmt.Print("Enter token Path or token: ")
		if _, err := fmt.Scanln(&token); err != nil {
			return fmt.Errorf("read token: %w", err)
		}
		token = strings.TrimSpace(token)
	}
	if token == "" {
		return fmt.Errorf("token cannot be empty")
	}

	path := tokenFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create token dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(token+"\n"), 0o600); err != nil {
		return fmt.Errorf("write token: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("save token: %w", err)
	}

	// If run by root and hubfly user exists, set ownership so service can read token.
	if os.Geteuid() == 0 {
		if u, err := user.Lookup("hubfly"); err == nil {
			uid, _ := strconv.Atoi(u.Uid)
			gid, _ := strconv.Atoi(u.Gid)
			_ = os.Chown(path, uid, gid)
			_ = os.Chmod(path, 0o600)
		}
	}

	fmt.Printf("Token initialized at %s\n", path)
	return nil
}

func lockdownFilePath() string {
	if p := strings.TrimSpace(os.Getenv("HTM_LOCKDOWN_FILE")); p != "" {
		return p
	}
	return "/hubfly-tool-manager/.lockdown.json"
}

func unlockLockdown(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("unlock does not take arguments")
	}
	path := lockdownFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create lockdown dir: %w", err)
	}
	payload := []byte("{\"locked\":false,\"failed_attempts\":0,\"updated_at\":\"" + time.Now().UTC().Format(time.RFC3339Nano) + "\"}\n")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o600); err != nil {
		return fmt.Errorf("write lockdown state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("save lockdown state: %w", err)
	}
	if os.Geteuid() == 0 {
		if u, err := user.Lookup("hubfly"); err == nil {
			uid, _ := strconv.Atoi(u.Uid)
			gid, _ := strconv.Atoi(u.Gid)
			_ = os.Chown(path, uid, gid)
			_ = os.Chmod(path, 0o600)
		}
	}
	fmt.Printf("Lockdown cleared in %s\n", path)
	return nil
}
