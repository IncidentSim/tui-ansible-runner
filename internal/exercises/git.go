package exercises

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"incidentctl/internal/config"
)

type GitSource struct {
	config config.ExerciseRepositoryConfig
	once   sync.Once
	local  *LocalSource
	err    error
}

func NewGitSource(cfg config.ExerciseRepositoryConfig) *GitSource {
	return &GitSource{config: cfg}
}

func (s *GitSource) List(ctx context.Context) ([]Item, error) {
	if err := s.prepare(ctx); err != nil {
		return nil, err
	}
	return s.local.List(ctx)
}

func (s *GitSource) Get(ctx context.Context, name string) (Item, error) {
	if err := s.prepare(ctx); err != nil {
		return Item{}, err
	}
	return s.local.Get(ctx, name)
}

func (s *GitSource) prepare(ctx context.Context) error {
	s.once.Do(func() {
		checkout, err := s.checkout(ctx)
		if err != nil {
			s.err = err
			return
		}
		root := filepath.Join(checkout, filepath.FromSlash(s.config.Subdirectory))
		info, err := os.Stat(root)
		if err != nil {
			s.err = fmt.Errorf("open exercise repository subdirectory: %w", err)
			return
		}
		if !info.IsDir() {
			s.err = fmt.Errorf("exercise repository subdirectory %s is not a directory", root)
			return
		}
		s.local = NewLocalSource(root)
	})
	return s.err
}

func (s *GitSource) checkout(ctx context.Context) (string, error) {
	if err := os.MkdirAll(s.config.CacheDir, 0o755); err != nil {
		return "", fmt.Errorf("create exercise cache: %w", err)
	}
	key := sha256.Sum256([]byte(s.config.Repository + "\x00" + s.config.Ref))
	checkout := filepath.Join(s.config.CacheDir, fmt.Sprintf("repository-%x", key[:8]))
	gitDir := filepath.Join(checkout, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		if err := os.MkdirAll(checkout, 0o755); err != nil {
			return "", fmt.Errorf("create repository checkout: %w", err)
		}
		if err := runGit(ctx, "-C", checkout, "init"); err != nil {
			return "", err
		}
		if err := runGit(ctx, "-C", checkout, "remote", "add", "origin", s.config.Repository); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", fmt.Errorf("inspect repository checkout: %w", err)
	}
	if err := runGit(ctx, "-C", checkout, "fetch", "--depth", "1", "origin", s.config.Ref); err != nil {
		return "", err
	}
	if err := runGit(ctx, "-C", checkout, "checkout", "--detach", "--force", "FETCH_HEAD"); err != nil {
		return "", err
	}
	return checkout, nil
}

func runGit(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return fmt.Errorf("git command failed: %w", err)
		}
		return fmt.Errorf("git command failed: %w: %s", err, message)
	}
	return nil
}
