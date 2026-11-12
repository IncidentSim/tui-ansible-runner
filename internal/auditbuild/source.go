// Package auditbuild resolves the standalone agent sources at TUI build time.
package auditbuild

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const Repository = "https://github.com/IncidentSim/audit-agent.git"

const Revision = "main"

// Source returns an explicit checkout or fetches the current remote revision.
// Completed checkouts are published atomically, so a failed fetch can be retried.
func Source(local, repository, revision, cache string) (string, error) {
	if local != "" {
		path, err := filepath.Abs(local)
		if err != nil {
			return "", err
		}
		return path, validate(path)
	}
	if repository == "" {
		repository = Repository
	}
	if revision == "" {
		revision = Revision
	}
	cache, err := filepath.Abs(cache)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(cache, 0755); err != nil {
		return "", err
	}
	staging, err := os.MkdirTemp(cache, ".fetch-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"remote", "add", "origin", repository},
		{"fetch", "--quiet", "--depth=1", "origin", revision},
		{"checkout", "--quiet", "--detach", "FETCH_HEAD"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = staging
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		output, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("download audit-agent (%s): %w\n%s", args[0], err, strings.TrimSpace(string(output)))
		}
	}
	if err := validate(staging); err != nil {
		return "", err
	}
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = staging
	commit, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve audit-agent revision: %w", err)
	}
	// Separate snapshots let branches advance without modifying running builds.
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(repository+"\n"+strings.TrimSpace(string(commit)))))
	destination := filepath.Join(cache, key)
	if validate(destination) == nil {
		return destination, nil
	}
	if err := os.Rename(staging, destination); err != nil {
		// Another build may have populated the same immutable cache meanwhile.
		if validate(destination) != nil {
			return "", fmt.Errorf("cache audit-agent: %w", err)
		}
	}
	return destination, nil
}

func validate(source string) error {
	for _, name := range []string{"go.mod", "cmd/incident-audit/main.go"} {
		info, err := os.Stat(filepath.Join(source, name))
		if err != nil {
			return fmt.Errorf("invalid audit-agent source: %w", err)
		}
		if info.IsDir() {
			return fmt.Errorf("invalid audit-agent source: %s is a directory", name)
		}
	}
	return nil
}
