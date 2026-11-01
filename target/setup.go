package target

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Init creates the workspace: origin.git seeded from the seed directory, with
// a post-receive hook that runs ciBin for each push to an ai/* branch. CI runs
// in the background so the push returns at once, as it would on GitHub.
func (l Local) Init(ctx context.Context, seed, ciBin string) error {
	if _, err := os.Stat(l.Origin()); err == nil {
		return fmt.Errorf("%s already exists", l.Origin())
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check origin: %w", err)
	}
	if _, err := git(ctx, "", "init", "-q", "--bare", "-b", "main", l.Origin()); err != nil {
		return err
	}

	src, err := os.MkdirTemp("", "aiw-seed-")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(src) }()
	if err := os.CopyFS(src, os.DirFS(seed)); err != nil {
		return fmt.Errorf("copy seed: %w", err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "-A"},
		{"-c", "user.name=" + gitName, "-c", "user.email=" + gitEmail, "commit", "-q", "-m", "Initial poster"},
		{"push", "-q", l.Origin(), "main"},
	} {
		if _, err := git(ctx, src, args...); err != nil {
			return err
		}
	}

	hook := fmt.Sprintf(`#!/bin/sh
# Installed by target setup. Runs CI for pushes to ai/* branches.
while read old new ref; do
  case "$ref" in
    refs/heads/ai/*) %s -target %s -ref "$ref" -sha "$new" </dev/null >>%s 2>&1 & ;;
  esac
done
`, strconv.Quote(ciBin), strconv.Quote(l.Dir), strconv.Quote(filepath.Join(l.Dir, "ci.log")))
	//nolint:gosec // a hook has to be executable
	if err := os.WriteFile(filepath.Join(l.Origin(), "hooks", "post-receive"), []byte(hook), 0o755); err != nil {
		return fmt.Errorf("install hook: %w", err)
	}
	return nil
}
