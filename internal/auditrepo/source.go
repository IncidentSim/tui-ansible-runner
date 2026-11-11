package auditrepo

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"incidentctl/internal/config"
)

// Source pins one downloaded revision for the entire CLI run. Concurrent runs
// download into separate staging directories and publish immutable checkouts.
type Source struct {
	Config config.ExerciseRepositoryConfig
	once   sync.Once
	root   string
	err    error
	git    func(context.Context, ...string) (string, error)
}

func (s *Source) Directory() (string, error) {
	s.once.Do(func() { s.root, s.err = s.checkout() })
	return s.root, s.err
}

func (s *Source) checkout() (string, error) {
	git := s.git
	if git == nil {
		git = runGit
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := os.MkdirAll(s.Config.CacheDir, 0700); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(s.Config.CacheDir, ".download-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	for _, args := range [][]string{
		{"init", stage},
		{"-C", stage, "remote", "add", "origin", s.Config.Repository},
		{"-C", stage, "fetch", "--depth", "1", "origin", s.Config.Ref},
	} {
		if _, err := git(ctx, args...); err != nil {
			return "", err
		}
	}
	commit, err := git(ctx, "-C", stage, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return "", err
	}
	commit = strings.TrimSpace(commit)
	if !regexp.MustCompile(`^[a-f0-9]{40,64}$`).MatchString(commit) {
		return "", fmt.Errorf("audit repository returned an invalid revision")
	}
	key := sha256.Sum256([]byte(s.Config.Repository))
	destination := filepath.Join(s.Config.CacheDir, fmt.Sprintf("repository-%x-%s", key[:8], commit))
	if _, err := os.Stat(destination); os.IsNotExist(err) {
		if _, err := git(ctx, "-C", stage, "checkout", "--detach", "--force", "FETCH_HEAD"); err != nil {
			return "", err
		}
		if err := os.Rename(stage, destination); err != nil {
			if info, statErr := os.Stat(destination); statErr != nil || !info.IsDir() {
				return "", fmt.Errorf("publish audit checkout: %w", err)
			}
		}
	} else if err != nil {
		return "", err
	}
	root := filepath.Join(destination, filepath.FromSlash(s.Config.Subdirectory))
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("audit repository subdirectory is not a directory")
	}
	return root, nil
}

func runGit(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Git diagnostics may contain credential-bearing repository URLs.
		if ctx.Err() != nil {
			return "", fmt.Errorf("download audit-agent: %w", ctx.Err())
		}
		return "", fmt.Errorf("download audit-agent: git failed; check repository access and ref: %w", err)
	}
	return string(output), nil
}
