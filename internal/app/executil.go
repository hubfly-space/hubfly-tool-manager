package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type CommandRunner struct {
	Timeout time.Duration
}

type Result struct {
	Stdout string
	Stderr string
}

func (r CommandRunner) Run(name string, args ...string) (Result, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	res := Result{Stdout: strings.TrimSpace(stdout.String()), Stderr: strings.TrimSpace(stderr.String())}
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return res, fmt.Errorf("command timeout after %s: %s %v", timeout, name, args)
		}
		return res, fmt.Errorf("command failed: %s %v: %w: %s", name, args, err, res.Stderr)
	}

	return res, nil
}
