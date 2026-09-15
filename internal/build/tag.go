package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/LittleDrongo/deployctl/internal/gitmeta"
)

type selectedTag struct {
	Info           gitmeta.Info
	Module, Prefix string
}

func git(ctx context.Context, root string, args ...string) (string, error) {
	cmd := command(ctx, root, nil, "git", args...)
	data, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(data)))
	}
	return strings.TrimSpace(string(data)), nil
}

func resolveTag(ctx context.Context, root, tag string) (selectedTag, error) {
	var selected selectedTag
	if _, err := git(ctx, root, "check-ref-format", "refs/tags/"+tag); err != nil {
		return selected, err
	}
	commit, err := git(ctx, root, "rev-parse", "--verify", "refs/tags/"+tag+"^{commit}")
	if err != nil {
		alias := "v" + tag
		if strings.HasPrefix(tag, "v") {
			alias = strings.TrimPrefix(tag, "v")
		}
		commit, err = git(ctx, root, "rev-parse", "--verify", "refs/tags/"+alias+"^{commit}")
		if err != nil {
			return selected, fmt.Errorf("local tag %q not found (also tried %q): %w", tag, alias, err)
		}
	}
	prefix, err := git(ctx, root, "rev-parse", "--show-prefix")
	if err != nil {
		return selected, err
	}
	mod, err := git(ctx, root, "show", commit+":"+prefix+"go.mod")
	if err != nil {
		return selected, fmt.Errorf("tag does not contain application go.mod: %w", err)
	}
	match := modulePattern.FindStringSubmatch(mod)
	if len(match) != 2 {
		return selected, fmt.Errorf("module directive missing at tag %s", tag)
	}
	selected.Module = strings.Trim(match[1], "\"")
	selected.Prefix = prefix
	short, err := git(ctx, root, "rev-parse", "--short", commit)
	if err != nil {
		return selected, err
	}
	date, err := git(ctx, root, "show", "-s", "--format=%cI", commit)
	if err != nil {
		return selected, err
	}
	selected.Info = gitmeta.Info{Version: tag, Tag: tag, Commit: commit, Short: short, CommitDate: date, HasGit: true}
	return selected, nil
}

func checkout(ctx context.Context, root string, selected selectedTag) (string, func() error, error) {
	base, err := os.MkdirTemp("", "deployctl-tag-")
	if err != nil {
		return "", nil, err
	}
	dir := filepath.Join(base, "source")
	if _, err = git(ctx, root, "worktree", "add", "--detach", dir, selected.Info.Commit); err != nil {
		os.Remove(base)
		return "", nil, err
	}
	cleanup := func() error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := git(cleanupCtx, root, "worktree", "remove", "--force", dir); err != nil {
			return fmt.Errorf("remove temporary checkout %s: %w", dir, err)
		}
		return os.Remove(base)
	}
	return filepath.Join(dir, filepath.FromSlash(selected.Prefix)), cleanup, nil
}
