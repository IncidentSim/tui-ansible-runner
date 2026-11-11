package exercises

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"incidentctl/internal/config"
)

type GitSource struct {
	config config.ExerciseRepositoryConfig
	once   sync.Once
	root   string
	err    error
}

func NewGitSource(cfg config.ExerciseRepositoryConfig) *GitSource {
	return &GitSource{config: cfg}
}

func (s *GitSource) List(ctx context.Context) ([]Item, error) {
	if err := s.prepare(ctx); err != nil {
		return nil, err
	}
	return listCatalog(ctx, s.root)
}

func (s *GitSource) Get(ctx context.Context, name string) (Item, error) {
	if err := s.prepare(ctx); err != nil {
		return Item{}, err
	}
	return loadCatalogItem(ctx, s.root, name)
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
		s.root = root
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

func listCatalog(ctx context.Context, root string) ([]Item, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read exercises: %w", err)
	}
	items := make([]Item, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, entry.Name(), "vm.json")); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("inspect exercise %s: %w", entry.Name(), err)
		}
		items = append(items, loadCatalogItemUnchecked(root, entry.Name()))
	}
	sort.Slice(items, func(i, j int) bool {
		left, right := items[i].Exercise.Name, items[j].Exercise.Name
		if left == "" {
			left = filepath.Base(items[i].Dir)
		}
		if right == "" {
			right = filepath.Base(items[j].Dir)
		}
		return left < right
	})
	return items, nil
}

func loadCatalogItem(ctx context.Context, root, name string) (Item, error) {
	if err := ctx.Err(); err != nil {
		return Item{}, err
	}
	item := loadCatalogItemUnchecked(root, name)
	if item.Err != nil {
		return Item{}, item.Err
	}
	return item, nil
}

func loadCatalogItemUnchecked(root, name string) Item {
	item := Item{Dir: filepath.Join(root, name)}
	item.Exercise, item.Dir, item.Err = config.LoadExercise(root, name)
	if data, err := os.ReadFile(filepath.Join(item.Dir, "README.md")); err == nil {
		item.Description = markdownToText(string(data))
	} else if item.Err == nil {
		item.Err = fmt.Errorf("read README.md: %w", err)
	}
	tips, err := loadTips(item.Dir)
	if err != nil && item.Err == nil {
		item.Err = err
	}
	item.Tips = tips
	return item
}

func loadTips(dir string) ([]Tip, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "tips", "*.md"))
	if err != nil {
		return nil, fmt.Errorf("list tips: %w", err)
	}
	sort.Strings(paths)
	tips := make([]Tip, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		body := markdownToText(string(data))
		title := firstLine(body)
		if title == "" {
			title = filepath.Base(path)
		}
		tips = append(tips, Tip{Title: title, Body: body})
	}
	return tips, nil
}

func markdownToText(value string) string {
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	inFence := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			lines[i] = ""
			continue
		}
		if !inFence {
			line = strings.TrimLeft(line, " \t")
			if strings.HasPrefix(line, "#") {
				line = strings.TrimSpace(strings.TrimLeft(line, "#"))
			}
		}
		lines[i] = line
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func firstLine(value string) string {
	for _, line := range strings.Split(value, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}
