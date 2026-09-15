// Package gitmeta reads application versions without changing Git state.
package gitmeta

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Info struct {
	Version    string `json:"version"`
	Commit     string `json:"commit"`
	Short      string `json:"short_commit"`
	CommitDate string `json:"commit_date"`
	Tag        string `json:"tag,omitempty"`
	Dirty      bool   `json:"dirty"`
	HasGit     bool   `json:"has_git"`
}

func output(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.WaitDelay = 2 * time.Second
	data, err := cmd.Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(data)), nil
}

func Read(ctx context.Context, dir string) (Info, error) {
	info := Info{Version: "dev", Commit: "unknown", Short: "unknown", CommitDate: "unknown"}
	// A .git file also counts: linked worktrees use it instead of a directory.
	dir, err := filepath.Abs(dir)
	if err != nil {
		return info, err
	}
	for parent := dir; ; parent = filepath.Dir(parent) {
		if _, err := os.Stat(filepath.Join(parent, ".git")); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return info, err
		}
		if filepath.Dir(parent) == parent {
			return info, nil
		}
	}
	inside, err := output(ctx, dir, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return info, err
	}
	if inside != "true" {
		return info, nil
	}
	info.HasGit = true
	if info.Commit, err = output(ctx, dir, "rev-parse", "--verify", "HEAD"); err != nil {
		return info, err
	}
	if info.Short, err = output(ctx, dir, "rev-parse", "--short", "HEAD"); err != nil {
		return info, err
	}
	if info.CommitDate, err = output(ctx, dir, "log", "-1", "--format=%cI"); err != nil {
		return info, err
	}
	status, err := output(ctx, dir, "status", "--porcelain")
	if err != nil {
		return info, err
	}
	info.Dirty = status != ""
	// Preserve the original Git log ordering, including lightweight tags.
	for skip := 0; ; skip += 10 {
		batch, err := output(ctx, dir, "log", "--decorate=short", "--decorate-refs=refs/tags", "--format=%H%x09%D", "-n", "10", "--skip", fmt.Sprint(skip), "HEAD")
		if err != nil {
			return info, err
		}
		lines := strings.Split(batch, "\n")
		for _, line := range lines {
			commit, decorations, _ := strings.Cut(line, "\t")
			for _, decoration := range strings.Split(decorations, ", ") {
				if tag, ok := strings.CutPrefix(decoration, "tag: "); ok {
					info.Tag, info.Version = tag, tag
					if commit != info.Commit {
						info.Version += "-dev"
					}
					if info.Dirty {
						info.Version += "-dirty"
					}
					return info, nil
				}
			}
		}
		if len(lines) < 10 {
			break
		}
	}
	info.Version = "dev-g" + info.Short
	if info.Dirty {
		info.Version += "-dirty"
	}
	return info, nil
}
