package pm2

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"hubfly-tool-manager/internal/app"
	"hubfly-tool-manager/internal/model"
)

type Client struct {
	runner app.CommandRunner
	pm2Bin string
	logger *log.Logger
}

type StartOptions struct {
	StdoutPath string
	StderrPath string
}

func New(pm2Bin string, timeout time.Duration, logger *log.Logger) *Client {
	if pm2Bin == "" {
		pm2Bin = "pm2"
	}
	return &Client{
		runner: app.CommandRunner{Timeout: timeout},
		pm2Bin: pm2Bin,
		logger: logger,
	}
}

func (c *Client) EnsureInstalled() error {
	if _, err := exec.LookPath(c.pm2Bin); err == nil {
		return nil
	}
	if _, err := exec.LookPath("npm"); err != nil {
		return fmt.Errorf("pm2 is not installed and npm not found: %w", err)
	}

	c.logger.Println("pm2 not found, attempting npm install -g pm2")
	if _, err := c.runner.Run("npm", "install", "-g", "pm2"); err != nil {
		return fmt.Errorf("install pm2 via npm: %w", err)
	}

	if _, err := exec.LookPath(c.pm2Bin); err != nil {
		return fmt.Errorf("pm2 still not found after install: %w", err)
	}
	return nil
}

func (c *Client) Status(name string) (string, error) {
	list, err := c.jlist()
	if err != nil {
		return "unknown", err
	}
	for _, p := range list {
		if p.Name == name {
			return p.PM2Env.Status, nil
		}
	}
	return "not_managed", nil
}

func (c *Client) StartOrReload(t model.ToolConfig, opts StartOptions) error {
	status, err := c.Status(t.Name)
	if err != nil {
		return err
	}

	if status != "not_managed" {
		if err := c.Stop(t.Name); err != nil {
			return err
		}
		if err := c.Delete(t.Name); err != nil {
			return err
		}
	}

	args := []string{"start", t.BinaryPath, "--name", t.Name, "--cwd", t.ToolDir, "--time"}
	if strings.TrimSpace(opts.StdoutPath) != "" {
		args = append(args, "--output", opts.StdoutPath)
	}
	if strings.TrimSpace(opts.StderrPath) != "" {
		args = append(args, "--error", opts.StderrPath)
	}
	if len(t.Args) > 0 {
		args = append(args, "--")
		args = append(args, t.Args...)
	}

	_, err = c.runner.Run(c.pm2Bin, args...)
	if err != nil {
		return fmt.Errorf("pm2 start: %w", err)
	}
	return c.waitUntilOnline(t.Name)
}

func (c *Client) Stop(name string) error {
	status, err := c.Status(name)
	if err != nil {
		return err
	}
	if status == "not_managed" {
		return nil
	}
	_, err = c.runner.Run(c.pm2Bin, "stop", name)
	if err != nil {
		if strings.Contains(err.Error(), "Process or Namespace") {
			return nil
		}
		return fmt.Errorf("pm2 stop: %w", err)
	}
	return nil
}

func (c *Client) Restart(name string) error {
	_, err := c.runner.Run(c.pm2Bin, "restart", name)
	if err != nil {
		return fmt.Errorf("pm2 restart: %w", err)
	}
	return nil
}

func (c *Client) Delete(name string) error {
	_, err := c.runner.Run(c.pm2Bin, "delete", name)
	if err != nil {
		if strings.Contains(err.Error(), "Process or Namespace") {
			return nil
		}
		return fmt.Errorf("pm2 delete: %w", err)
	}
	return nil
}

func (c *Client) Save() error {
	_, err := c.runner.Run(c.pm2Bin, "save")
	if err != nil {
		return fmt.Errorf("pm2 save: %w", err)
	}
	return nil
}

func (c *Client) waitUntilOnline(name string) error {
	deadline := time.Now().Add(8 * time.Second)
	lastStatus := "unknown"

	for time.Now().Before(deadline) {
		status, err := c.Status(name)
		if err != nil {
			return err
		}
		lastStatus = status
		switch status {
		case "online":
			return nil
		case "errored", "stopped":
			return fmt.Errorf("pm2 process %s status=%s", name, status)
		}
		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf("pm2 process %s did not become online (last status=%s)", name, lastStatus)
}

type jlistEntry struct {
	Name   string `json:"name"`
	PM2Env struct {
		Status string `json:"status"`
	} `json:"pm2_env"`
}

func (c *Client) jlist() ([]jlistEntry, error) {
	res, err := c.runner.Run(c.pm2Bin, "jlist")
	if err != nil {
		if strings.Contains(err.Error(), "No process found") {
			return []jlistEntry{}, nil
		}
		return nil, fmt.Errorf("pm2 jlist: %w", err)
	}
	if res.Stdout == "" {
		return []jlistEntry{}, nil
	}
	var list []jlistEntry
	if err := json.Unmarshal([]byte(res.Stdout), &list); err != nil {
		return nil, errors.New("failed to parse pm2 jlist output")
	}
	return list, nil
}
