package exercises

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"incidentctl/internal/config"
)

type LocalSource struct {
	root string
}

func NewLocalSource(root string) *LocalSource {
	return &LocalSource{root: root}
}

func (s *LocalSource) List(ctx context.Context) ([]Item, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.root)
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
		if _, err := os.Stat(filepath.Join(s.root, entry.Name(), "vm.json")); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("inspect exercise %s: %w", entry.Name(), err)
		}
		items = append(items, s.load(entry.Name()))
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

func (s *LocalSource) Get(ctx context.Context, name string) (Item, error) {
	if err := ctx.Err(); err != nil {
		return Item{}, err
	}
	item := s.load(name)
	if item.Err != nil {
		return Item{}, item.Err
	}
	return item, nil
}

func (s *LocalSource) load(name string) Item {
	item := Item{Dir: filepath.Join(s.root, name)}
	item.Exercise, item.Dir, item.Err = config.LoadExercise(s.root, name)
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
