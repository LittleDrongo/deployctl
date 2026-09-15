package deploy

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/LittleDrongo/deployctl/internal/config"
)

// OpenSSH uses the system clients and their normal per-user SSH configuration.
type OpenSSH struct{ Out io.Writer }

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

func (s OpenSSH) run(ctx context.Context, t config.Target, duration time.Duration, input io.Reader, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdin = input
	output := newRedactor(s.Out, t)
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

func (s OpenSSH) Shell(ctx context.Context, t config.Target, script, phase string) error {
	if _, err := fmt.Fprintf(s.Out, "remote : %s — %s\n", t.Host, phase); err != nil {
		return err
	}
	args := append(sshArgs(t, false), destination(t, false), fmt.Sprintf("exec timeout --signal=TERM --kill-after=60s %ds sh -s", t.SSHTimeout))
	return s.run(ctx, t, time.Duration(t.SSHTimeout+90)*time.Second, strings.NewReader(script), "ssh", args...)
}

func (s OpenSSH) Upload(ctx context.Context, t config.Target, local, remote string) error {
	// SFTP paths are literal, not remote-shell quoted. Do not force legacy SCP.
	args := append(sshArgs(t, true), local, destination(t, true)+":"+remote)
	return s.run(ctx, t, 5*time.Minute, nil, "scp", args...)
}
