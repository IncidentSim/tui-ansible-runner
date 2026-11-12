package auditbuild

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, directory string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = directory
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func fixture(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "cmd", "incident-audit"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"go.mod":                     "module test-agent\n\ngo 1.23\n",
		"cmd/incident-audit/main.go": "package main\nfunc main() {}\n",
	} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "init", "--quiet")
	git(t, repo, "add", ".")
	git(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "fixture")
	return repo, git(t, repo, "rev-parse", "HEAD")
}

func TestDownloadLatestMainAndRefreshCache(t *testing.T) {
	repo, revision := fixture(t)
	git(t, repo, "branch", "-M", "main")
	cache := t.TempDir()
	source, err := Source("", repo, "", cache)
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, source, "rev-parse", "HEAD"); got != revision {
		t.Fatalf("got revision %s, want %s", got, revision)
	}
	if cached, err := Source("", repo, "", cache); err != nil || cached != source {
		t.Fatalf("cached source %q: %v", cached, err)
	}
	git(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--quiet", "--allow-empty", "-m", "update main")
	latest := git(t, repo, "rev-parse", "HEAD")
	updated, err := Source("", repo, "", cache)
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, updated, "rev-parse", "HEAD"); got != latest {
		t.Fatalf("got revision %s, want latest %s", got, latest)
	}
	if updated == source {
		t.Fatal("branch update reused outdated source")
	}
	if got := git(t, source, "rev-parse", "HEAD"); got != revision {
		t.Fatal("updating branch modified an existing build snapshot")
	}
}

func TestFailedFetchCanRetry(t *testing.T) {
	repo, revision := fixture(t)
	cache := t.TempDir()
	if _, err := Source("", repo, "missing-ref", cache); err == nil {
		t.Fatal("expected missing ref error")
	}
	entries, err := os.ReadDir(cache)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed fetch left cache entries: %v, %v", entries, err)
	}
	if _, err := Source("", repo, revision, cache); err != nil {
		t.Fatal(err)
	}
}

func TestLocalOverrideDoesNotDownload(t *testing.T) {
	repo, _ := fixture(t)
	if source, err := Source(repo, "invalid-repository", "missing-ref", t.TempDir()); err != nil || source != repo {
		t.Fatalf("local override %q: %v", source, err)
	}
	if _, err := Source(t.TempDir(), "", "", t.TempDir()); err == nil {
		t.Fatal("expected invalid local checkout error")
	}
}
