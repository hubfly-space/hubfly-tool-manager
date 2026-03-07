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
	case "health":
		err = doGet(baseURL, "/health")
	case "register":
		err = register(baseURL, args)
	case "list":
		err = doGet(baseURL, "/tools")
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
  htm rollback <tool> [backup_id]
  htm cleanup <tool>
  htm self-update <work_dir> [command...]

Env:
  HTM_SERVER   default: http://127.0.0.1:10000`)
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
	return doPost(baseURL, "/tools/register", body)
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
	return doGet(baseURL, path)
}

func selfUpdate(baseURL string, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("missing work_dir")
	}
	body := map[string]any{"work_dir": args[0]}
	if len(args) > 1 {
		body["update_command"] = args[1:]
	}
	return doPost(baseURL, "/self/update", body)
}

func rollback(baseURL string, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("missing tool name")
	}
	body := map[string]any{}
	if len(args) > 1 {
		body["backup_id"] = args[1]
	}
	return doPost(baseURL, "/tools/"+url.PathEscape(args[0])+"/rollback", body)
}

func requireToolAndGet(baseURL string, args []string, suffix string) error {
	if len(args) < 1 {
		return fmt.Errorf("missing tool name")
	}
	path := "/tools/" + url.PathEscape(args[0]) + suffix
	return doGet(baseURL, path)
}

func requireToolAndPost(baseURL string, args []string, suffix string, body any) error {
	if len(args) < 1 {
		return fmt.Errorf("missing tool name")
	}
	path := "/tools/" + url.PathEscape(args[0]) + suffix
	return doPost(baseURL, path, body)
}

func doGet(baseURL, path string) error {
	resp, err := httpClient().Get(strings.TrimRight(baseURL, "/") + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return printResponse(resp)
}

func doPost(baseURL, path string, body any) error {
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
