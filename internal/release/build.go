package release

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"incidentctl/internal/auditbuild"
	"incidentctl/internal/config"
)

const DefaultTargets = "windows/amd64,linux/amd64,darwin/amd64,darwin/arm64"

var releaseVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$`)

type Options struct {
	Root, Version, Targets, Output string
}

type target struct{ goos, arch string }

func (t target) name() string { return t.goos + "_" + t.arch }
func (t target) executable(name string) string {
	if t.goos == "windows" {
		return name + ".exe"
	}
	return name
}

func parseTargets(value string) ([]target, error) {
	if value == "" {
		value = DefaultTargets
	}
	seen, targets := map[string]bool{}, []target{}
	for _, value := range strings.Split(value, ",") {
		if !strings.Contains(","+DefaultTargets+",", ","+value+",") {
			return nil, fmt.Errorf("unsupported release target %q", value)
		}
		if seen[value] {
			return nil, fmt.Errorf("duplicate release target %q", value)
		}
		seen[value] = true
		goos, arch, _ := strings.Cut(value, "/")
		targets = append(targets, target{goos, arch})
	}
	return targets, nil
}

// Build fetches the agent once, then compiles all targets from that same
// checkout. Each TUI gets only its native agent and the Linux VM agent.
func Build(o Options) (string, error) {
	if !releaseVersion.MatchString(o.Version) {
		return "", errors.New("version must be a tag such as v0.1.0 or v0.1.0-rc.1")
	}
	targets, err := parseTargets(o.Targets)
	if err != nil {
		return "", err
	}
	root, err := filepath.Abs(o.Root)
	if err != nil {
		return "", err
	}
	if o.Output == "" {
		o.Output = filepath.Join(root, "dist", o.Version)
	}
	output, err := filepath.Abs(o.Output)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		return "", fmt.Errorf("release output already exists or is inaccessible: %s", output)
	}
	readme := filepath.Join(root, "distribution", "README.md")
	if _, err := os.Stat(readme); err != nil {
		return "", err
	}
	example := filepath.Join(root, "incidentctl.example.json")
	cfg, err := config.Load(example)
	if err != nil {
		return "", fmt.Errorf("distribution configuration: %w", err)
	}
	if cfg.Audit.LLM == nil || cfg.Audit.LLM.Enabled || cfg.Audit.LLM.APIKey != "" || cfg.TokenFile != "" {
		return "", errors.New("distribution example must disable LLM and contain no API key or token file")
	}
	if err := os.MkdirAll(filepath.Dir(output), 0755); err != nil {
		return "", err
	}
	assets, err := os.MkdirTemp(filepath.Dir(output), ".release-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(assets)
	workspace, err := os.MkdirTemp("", "incidentctl-release-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(workspace)
	if err := copySources(root, workspace); err != nil {
		return "", err
	}
	source, err := auditbuild.Source(os.Getenv("AUDIT_AGENT_DIR"), os.Getenv("AUDIT_AGENT_REPOSITORY"), os.Getenv("AUDIT_AGENT_REF"), filepath.Join(root, ".cache", "audit-agent"))
	if err != nil {
		return "", err
	}
	revision, agentRevision := gitRevision(root), gitRevision(source)
	binDir := filepath.Join(workspace, "internal", "auditbinary", "binaries")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return "", err
	}
	linuxAgent := filepath.Join(binDir, "linux-amd64")
	if err := auditbuild.Build(source, linuxAgent, "linux", "amd64"); err != nil {
		return "", err
	}
	archives := []string{}
	for _, target := range targets {
		fmt.Println("Building distribution", target.name())
		nativeAgent := filepath.Join(binDir, target.executable(target.goos+"-"+target.arch))
		if nativeAgent != linuxAgent {
			if err := auditbuild.Build(source, nativeAgent, target.goos, target.arch); err != nil {
				return "", err
			}
		}
		binary := filepath.Join(workspace, target.executable("incidentctl"))
		ldflags := fmt.Sprintf("-s -w -X main.version=%s -X main.revision=%s -X main.auditRevision=%s", o.Version, revision, agentRevision)
		cmd := exec.Command("go", "build", "-buildvcs=false", "-trimpath", "-ldflags="+ldflags, "-o", binary, "./cmd/incidentctl")
		cmd.Dir, cmd.Env = workspace, auditbuild.Environment(target.goos, target.arch)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("build TUI %s: %w", target.name(), err)
		}
		extension := ".tar.gz"
		if target.goos == "windows" {
			extension = ".zip"
		}
		name := "incidentctl_" + o.Version + "_" + target.name() + extension
		files := []archiveFile{{target.executable("incidentctl"), binary, 0755}, {"README.md", readme, 0644}, {"incidentctl.example.json", example, 0644}}
		if err := writeArchive(filepath.Join(assets, name), files, target.goos == "windows"); err != nil {
			return "", err
		}
		archives = append(archives, name)
		if nativeAgent != linuxAgent {
			if err := os.Remove(nativeAgent); err != nil {
				return "", err
			}
		}
	}
	metadata, err := json.MarshalIndent(map[string]any{"version": o.Version, "tui_revision": revision, "audit_agent_revision": agentRevision, "go_version": runtime.Version(), "archives": archives}, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(assets, "build-info.json"), append(metadata, '\n'), 0644); err != nil {
		return "", err
	}
	if err := checksums(assets, append(archives, "build-info.json")); err != nil {
		return "", err
	}
	if err := os.Rename(assets, output); err != nil {
		return "", err
	}
	return output, nil
}

func gitRevision(directory string) string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = directory
	output, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	revision := strings.TrimSpace(string(output))
	cmd = exec.Command("git", "status", "--porcelain", "--untracked-files=normal")
	cmd.Dir = directory
	if status, err := cmd.Output(); err == nil && len(status) > 0 {
		revision += "-dirty"
	}
	return revision
}

func copySources(root, destination string) error {
	for _, name := range []string{"go.mod", "go.sum", "cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, name), func(source string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(root, source)
			if err != nil {
				return err
			}
			if filepath.ToSlash(relative) == "internal/auditbinary/binaries" {
				return filepath.SkipDir
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("symlink in build sources: %s", relative)
			}
			target := filepath.Join(destination, relative)
			if entry.IsDir() {
				return os.MkdirAll(target, 0755)
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
			if err != nil {
				return err
			}
			err = copyFileTo(file, source)
			closeErr := file.Close()
			if err != nil {
				return err
			}
			return closeErr
		})
		if err != nil {
			return err
		}
	}
	return nil
}
