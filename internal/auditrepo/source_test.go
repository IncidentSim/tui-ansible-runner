package auditrepo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"incidentctl/internal/config"
)

func TestSourcePinsDownloadedRevisionAndKeepsImmutableCache(t *testing.T) {
	cache := t.TempDir()
	cfg := config.ExerciseRepositoryConfig{Repository: "git@example.invalid:agent.git", Ref: "main", CacheDir: cache, Subdirectory: "."}
	fetches, checkouts := 0, 0
	runner := func(_ context.Context, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "fetch --depth 1 origin main") {
			fetches++
		}
		if strings.Contains(joined, "rev-parse") {
			return strings.Repeat("a", 40) + "\n", nil
		}
		if strings.Contains(joined, "checkout --detach --force FETCH_HEAD") {
			checkouts++
			return "", os.WriteFile(filepath.Join(args[1], "install.py"), []byte("downloaded installer"), 0600)
		}
		return "", nil
	}
	source := &Source{Config: cfg, git: runner}
	root, err := source.Directory()
	if err != nil {
		t.Fatal(err)
	}
	again, err := source.Directory()
	if err != nil || again != root || fetches != 1 {
		t.Fatal("revision not pinned for CLI lifetime")
	}
	second := &Source{Config: cfg, git: runner}
	next, err := second.Directory()
	if err != nil || next != root || checkouts != 1 || fetches != 2 {
		t.Fatalf("immutable cache not reused: %s %v", next, err)
	}
	data, err := os.ReadFile(filepath.Join(root, "install.py"))
	if err != nil || string(data) != "downloaded installer" {
		t.Fatal("downloaded files unavailable")
	}
	staging, _ := filepath.Glob(filepath.Join(cache, ".download-*"))
	if len(staging) != 0 {
		t.Fatal("staging directories not cleaned up")
	}
}

func TestDownloadFailureDoesNotPublishPartialCheckout(t *testing.T) {
	cache := t.TempDir()
	source := &Source{Config: config.ExerciseRepositoryConfig{CacheDir: cache}, git: func(context.Context, ...string) (string, error) { return "", errors.New("network unavailable") }}
	if _, err := source.Directory(); err == nil {
		t.Fatal("expected download failure")
	}
	entries, err := os.ReadDir(cache)
	if err != nil || len(entries) != 0 {
		t.Fatal("partial download left in cache")
	}
}
