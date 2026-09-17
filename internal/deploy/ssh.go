package deploy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/LittleDrongo/deployctl/internal/config"
	"github.com/LittleDrongo/deployctl/internal/terminal"
)

// OpenSSH uses the system clients and their normal per-user SSH configuration.
type OpenSSH struct{ Out io.Writer }

// CheckLocalClients verifies the two system clients used by remote operations.
func CheckLocalClients() error {
	for _, name := range []string{"ssh", "scp"} {
		if _, err := exec.LookPath(name); err != nil {
			return fmt.Errorf("required OpenSSH client %q is not available in PATH: %w", name, err)
		}
	}
	return nil
}

func sshArgs(t config.Target, scp bool) []string {
	args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3"}
	if t.Port != nil {
		flag := "-p"
		if scp {
			flag = "-P"
		}
		args = append(args, flag, strconv.Itoa(*t.Port))
	}
	return args
}

func destination(t config.Target, scp bool) string {
	host := t.Host
	if scp && strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if t.User != "" {
		return t.User + "@" + host
	}
	return host
}

func (s OpenSSH) run(ctx context.Context, t config.Target, duration time.Duration, input io.Reader, phase, name string, args ...string) error {
	if duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, duration)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdin = input
	output := newRedactor(terminal.Remote(s.Out, phase), t)
	cmd.Stdout, cmd.Stderr = output, output
	err := cmd.Run()
	flushErr := output.Flush()
	if ctx.Err() != nil {
		return fmt.Errorf("%s: %w", name, ctx.Err())
	}
	if err != nil {
		return fmt.Errorf("%s: %w (diagnostics above)", name, err)
	}
	return flushErr
}

// Capture reads only the structured status response from stdout. SSH and
// Docker diagnostics go through the usual redactor on stderr.
func (s OpenSSH) Capture(ctx context.Context, t config.Target, script string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(t.SSHTimeout+90)*time.Second)
	defer cancel()
	args := append(sshArgs(t, false), destination(t, false), fmt.Sprintf("exec timeout --signal=TERM --kill-after=60s %ds sh -s", t.SSHTimeout))
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdin = strings.NewReader(script)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	stderr := newRedactor(s.Out, t)
	cmd.Stderr = stderr
	err := cmd.Run()
	flushErr := stderr.Flush()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("ssh: %w", ctx.Err())
	}
	if err != nil {
		return nil, fmt.Errorf("ssh: %w (diagnostics above)", err)
	}
	if flushErr != nil {
		return nil, flushErr
	}
	return stdout.Bytes(), nil
}

// Stream runs a remote shell until the caller cancels the context. It is used
// for commands such as logs --follow which intentionally have no fixed timeout.
func (s OpenSSH) Stream(ctx context.Context, t config.Target, script, phase string) error {
	if _, err := fmt.Fprintf(s.Out, "remote : %s — %s\n", t.Host, phase); err != nil {
		return err
	}
	args := append(sshArgs(t, false), destination(t, false), "exec sh -s")
	return s.run(ctx, t, 0, strings.NewReader(script), phase, "ssh", args...)
}

func (s OpenSSH) Shell(ctx context.Context, t config.Target, script, phase string) error {
	if _, err := fmt.Fprintf(s.Out, "remote : %s — %s\n", t.Host, phase); err != nil {
		return err
	}
	args := append(sshArgs(t, false), destination(t, false), fmt.Sprintf("exec timeout --signal=TERM --kill-after=60s %ds sh -s", t.SSHTimeout))
	return s.run(ctx, t, time.Duration(t.SSHTimeout+90)*time.Second, strings.NewReader(script), phase, "ssh", args...)
}

func (s OpenSSH) Upload(ctx context.Context, t config.Target, local, remote string) error {
	// SFTP paths are literal, not remote-shell quoted. Do not force legacy SCP.
	args := append(sshArgs(t, true), local, destination(t, true)+":"+remote)
	return s.run(ctx, t, 5*time.Minute, nil, "upload", "scp", args...)
}
