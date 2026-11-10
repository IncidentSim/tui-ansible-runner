package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var safeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)

type Config struct {
	Target TargetConfig `json:"target"`
	Paths  PathsConfig  `json:"paths"`
}

type TargetConfig struct {
	Host                  string `json:"host"`
	Port                  int    `json:"port"`
	User                  string `json:"user"`
	IdentityFile          string `json:"identity_file"`
	StrictHostKeyChecking bool   `json:"strict_host_key_checking"`
}

type PathsConfig struct {
	RemoteWorkDir string `json:"remote_work_dir"`
}

type Exercise struct {
	SchemaVersion int             `json:"schema_version"`
	Name          string          `json:"name"`
	Resources     Resources       `json:"resources"`
	Runtime       Runtime         `json:"runtime"`
	Playbooks     ExerciseActions `json:"playbooks"`
}

type Resources struct {
	VCPUCount int `json:"vcpu_count"`
	MemoryMiB int `json:"memory_mib"`
}

type Runtime struct {
	OS                OperatingSystem `json:"os"`
	BootstrapPackages []Package       `json:"bootstrap_packages"`
	ManagedPackages   []Package       `json:"managed_packages"`
}

type OperatingSystem struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

type Package struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type ExerciseActions struct {
	Infra        string `json:"infra"`
	Incident     string `json:"incident"`
	Verification string `json:"verification"`
}

func Load(path string) (Config, error) {
	var cfg Config
	if err := decode(path, &cfg); err != nil {
		return cfg, err
	}
	if cfg.Target.Port == 0 {
		cfg.Target.Port = 22
	}
	if cfg.Paths.RemoteWorkDir == "" {
		cfg.Paths.RemoteWorkDir = "/var/tmp/incidentctl/exercises"
	}
	identityFile, err := expandHome(cfg.Target.IdentityFile)
	if err != nil {
		return cfg, err
	}
	cfg.Target.IdentityFile = identityFile
	return cfg, cfg.Validate()
}

func LoadExercise(root, name string) (Exercise, string, error) {
	var exercise Exercise
	if !SafeName(name) {
		return exercise, "", fmt.Errorf("invalid exercise name %q", name)
	}
	dir := filepath.Join(root, name)
	if err := decode(filepath.Join(dir, "vm.json"), &exercise); err != nil {
		return exercise, dir, err
	}
	if exercise.Name != name {
		return exercise, dir, fmt.Errorf("vm.json name must match directory name %q", name)
	}
	if err := exercise.Validate(dir); err != nil {
		return exercise, dir, err
	}
	return exercise, dir, nil
}

func (c Config) Validate() error {
	missing := make([]string, 0, 4)
	for key, value := range map[string]string{
		"target.host":           c.Target.Host,
		"target.user":           c.Target.User,
		"target.identity_file":  c.Target.IdentityFile,
		"paths.remote_work_dir": c.Paths.RemoteWorkDir,
	} {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, key)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%s must not contain line breaks", key)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required configuration: %s", strings.Join(missing, ", "))
	}
	if c.Target.Port < 1 || c.Target.Port > 65535 {
		return errors.New("target.port must be between 1 and 65535")
	}
	if !SafeName(c.Target.User) {
		return errors.New("target.user contains unsupported characters")
	}
	if !safeAbsolutePath(c.Target.IdentityFile) {
		return errors.New("target.identity_file must be an absolute path other than /")
	}
	info, err := os.Stat(c.Target.IdentityFile)
	if err != nil {
		return fmt.Errorf("target.identity_file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("target.identity_file must be a regular file")
	}
	if !safeAbsolutePath(c.Paths.RemoteWorkDir) {
		return errors.New("paths.remote_work_dir must be an absolute path other than /")
	}
	return nil
}

func (e Exercise) Validate(dir string) error {
	if e.SchemaVersion != 1 {
		return fmt.Errorf("unsupported vm.json schema_version %d", e.SchemaVersion)
	}
	if !SafeName(e.Name) {
		return errors.New("name must contain only letters, digits, dot, underscore, and dash")
	}
	if e.Resources.VCPUCount < 1 || e.Resources.MemoryMiB < 128 {
		return errors.New("resources must define at least 1 vCPU and 128 MiB of memory")
	}
	if e.Runtime.OS.ID == "" || e.Runtime.OS.Version == "" {
		return errors.New("runtime.os.id and runtime.os.version are required")
	}
	if err := validatePackages("bootstrap_packages", e.Runtime.BootstrapPackages); err != nil {
		return err
	}
	if err := validatePackages("managed_packages", e.Runtime.ManagedPackages); err != nil {
		return err
	}
	for action, playbook := range map[string]string{
		"infra":        e.Playbooks.Infra,
		"incident":     e.Playbooks.Incident,
		"verification": e.Playbooks.Verification,
	} {
		if err := validatePlaybook(dir, action, playbook); err != nil {
			return err
		}
	}
	return nil
}

func SafeName(value string) bool { return safeName.MatchString(value) }

func validatePackages(group string, packages []Package) error {
	seen := make(map[string]struct{}, len(packages))
	for _, pkg := range packages {
		if strings.TrimSpace(pkg.Name) == "" || strings.TrimSpace(pkg.Version) == "" {
			return fmt.Errorf("runtime.%s entries require name and version", group)
		}
		if strings.ContainsAny(pkg.Name+pkg.Version, "\r\n") {
			return fmt.Errorf("runtime.%s entries must not contain line breaks", group)
		}
		if _, exists := seen[pkg.Name]; exists {
			return fmt.Errorf("runtime.%s contains duplicate package %q", group, pkg.Name)
		}
		seen[pkg.Name] = struct{}{}
	}
	return nil
}

func validatePlaybook(dir, action, playbook string) error {
	if strings.TrimSpace(playbook) == "" {
		return fmt.Errorf("playbooks.%s is required", action)
	}
	clean := filepath.Clean(playbook)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(filepath.ToSlash(clean), "../") {
		return fmt.Errorf("playbooks.%s must be a relative path inside the exercise", action)
	}
	playbookPath := filepath.Join(dir, clean)
	info, err := os.Stat(playbookPath)
	if err != nil {
		return fmt.Errorf("playbooks.%s %s: %w", action, playbookPath, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("playbooks.%s %s is not a regular file", action, playbookPath)
	}
	return nil
}

func safeAbsolutePath(value string) bool {
	clean := filepath.Clean(value)
	return filepath.IsAbs(value) && clean != string(filepath.Separator)
}

func expandHome(value string) (string, error) {
	if value == "~" || strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		if value == "~" {
			return home, nil
		}
		return filepath.Join(home, strings.TrimPrefix(value, "~/")), nil
	}
	return value, nil
}

func decode(path string, target any) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode %s: trailing JSON data", path)
	}
	return nil
}
