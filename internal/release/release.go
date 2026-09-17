// Package release creates and publishes application release tags.
package release

import (
	"context"
	"fmt"
	"io"
	"math/big"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	batchSize    = 10
	commandLimit = 30 * time.Second
	releaseLimit = 5 * time.Minute
)

var releasePattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.([0-9]{1,2})$`)

type version struct {
	major, minor *big.Int
	patch        int
}

func parseVersion(tag string) (version, bool) {
	match := releasePattern.FindStringSubmatch(tag)
	if match == nil {
		return version{}, false
	}
	major, ok := new(big.Int).SetString(match[1], 10)
	if !ok {
		return version{}, false
	}
	minor, ok := new(big.Int).SetString(match[2], 10)
	if !ok {
		return version{}, false
	}
	patch, err := strconv.Atoi(match[3])
	if err != nil {
		return version{}, false
	}
	return version{major: major, minor: minor, patch: patch}, true
}

func (v version) less(other version) bool {
	if compared := v.major.Cmp(other.major); compared != 0 {
		return compared < 0
	}
	if compared := v.minor.Cmp(other.minor); compared != 0 {
		return compared < 0
	}
	return v.patch < other.patch
}

func next(tag string) string {
	v, ok := parseVersion(tag)
	if !ok {
		return "v1.0.0"
	}
	v.patch++
	if v.patch == 100 {
		v.patch = 0
		v.minor.Add(v.minor, big.NewInt(1))
	}
	return fmt.Sprintf("v%s.%s.%d", v.major.String(), v.minor.String(), v.patch)
}

type gitRunner func(context.Context, ...string) (string, error)

func command(root string) gitRunner {
	return func(parent context.Context, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(parent, commandLimit)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
		cmd.WaitDelay = 2 * time.Second
		data, err := cmd.CombinedOutput()
		text := strings.TrimSpace(string(data))
		if ctx.Err() != nil {
			return text, ctx.Err()
		}
		if err != nil {
			if text != "" {
				return text, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, text)
			}
			return text, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return text, nil
	}
}

// nearestRelease walks reachable commits from HEAD in date order, ten at a
// time. It stops at the first commit carrying a release tag.
func nearestRelease(ctx context.Context, run gitRunner) (string, error) {
	for skip := 0; ; skip += batchSize {
		output, err := run(ctx, "log", "--date-order", "--decorate=short", "--decorate-refs=refs/tags", "--format=%H%x09%D", "-n", strconv.Itoa(batchSize), "--skip", strconv.Itoa(skip), "HEAD")
		if err != nil {
			return "", err
		}
		if output == "" {
			return "", nil
		}
		lines := strings.Split(output, "\n")
		for _, line := range lines {
			_, decorations, found := strings.Cut(line, "\t")
			if !found {
				continue
			}
			var selected string
			var selectedVersion version
			for _, decoration := range strings.Split(decorations, ", ") {
				tag, ok := strings.CutPrefix(decoration, "tag: ")
				if !ok {
					continue
				}
				candidate, ok := parseVersion(tag)
				if ok && (selected == "" || selectedVersion.less(candidate)) {
					selected, selectedVersion = tag, candidate
				}
			}
			if selected != "" {
				return selected, nil
			}
		}
		if len(lines) < batchSize {
			return "", nil
		}
	}
}

type Options struct {
	Root   string
	DryRun bool
}

// Run validates one immutable HEAD, creates its next release tag and pushes
// only that tag when origin exists. A failed push deliberately leaves the local
// tag; repositories without origin can create local releases successfully.
func Run(parent context.Context, options Options, out io.Writer) error {
	ctx, cancel := context.WithTimeout(parent, releaseLimit)
	defer cancel()
	run := command(options.Root)
	head, err := run(ctx, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return fmt.Errorf("release requires a Git repository with a commit: %w", err)
	}
	status, err := run(ctx, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("read working tree: %w", err)
	}
	if status != "" {
		return fmt.Errorf("release requires a clean working tree; commit or stash changes first")
	}
	current, err := run(ctx, "tag", "--points-at", head)
	if err != nil {
		return fmt.Errorf("read HEAD tags: %w", err)
	}
	for _, tag := range strings.Fields(current) {
		if releasePattern.MatchString(tag) {
			return fmt.Errorf("current commit already has release tag %s; create a new commit first", tag)
		}
	}
	previous, err := nearestRelease(ctx, run)
	if err != nil {
		return fmt.Errorf("find nearest release tag: %w", err)
	}
	nextTag := next(previous)
	if options.DryRun {
		present, err := hasOrigin(ctx, run)
		if err != nil {
			return err
		}
		publication := "origin отсутствует: тег останется локальным"
		if present {
			publication = "тег будет отправлен в origin"
		}
		if _, err := fmt.Fprintln(out, "Публикация:", publication); err != nil {
			return err
		}
		if previous == "" {
			previous = "не найден"
		}
		_, err = fmt.Fprintf(out, "HEAD: %s\nБлижайший релизный тег: %s\nСледующий тег: %s\nDry-run: тег не создан и не отправлен.\n", head, previous, nextTag)
		return err
	}
	// Bind the tag to the inspected commit, even if HEAD changes concurrently.
	if _, err := run(ctx, "-c", "tag.gpgSign=false", "tag", nextTag, head); err != nil {
		return fmt.Errorf("create tag %s: %w", nextTag, err)
	}
	if _, err := fmt.Fprintf(out, "Создан локальный тег %s → %s\n", nextTag, head); err != nil {
		return err
	}
	present, err := hasOrigin(ctx, run)
	if err != nil {
		return fmt.Errorf("local tag %s preserved; %w", nextTag, err)
	}
	if !present {
		_, err := fmt.Fprintf(out, "origin отсутствует; тег %s сохранён локально, отправка пропущена.\n", nextTag)
		return err
	}
	ref := "refs/tags/" + nextTag
	if _, err := run(ctx, "push", "--no-follow-tags", "origin", ref+":"+ref); err != nil {
		return fmt.Errorf("push tag %s to origin failed: %w; local tag preserved; retry: git push --no-follow-tags origin %s:%s", nextTag, err, ref, ref)
	}
	_, err = fmt.Fprintf(out, "Тег %s опубликован в origin\n", nextTag)
	return err
}

// Distinguish an absent origin from an error reading Git configuration.
func hasOrigin(ctx context.Context, run gitRunner) (bool, error) {
	remotes, err := run(ctx, "remote")
	if err != nil {
		return false, fmt.Errorf("read Git remotes: %w", err)
	}
	for _, remote := range strings.Fields(remotes) {
		if remote == "origin" {
			return true, nil
		}
	}
	return false, nil
}
