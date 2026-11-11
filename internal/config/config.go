package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var safeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)
var safeEnvironmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type Config struct {
	ExerciseRepository ExerciseRepositoryConfig `json:"exercise_repository"`
	VMService          MicroVMServiceConfig     `json:"vm_service"`
	UserIDFile         string                   `json:"user_id_file,omitempty"`
}

type MicroVMServiceConfig struct {
	BaseURL        string `json:"base_url"`
	TokenEnv       string `json:"token_env"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	RemoteWorkDir  string `json:"remote_work_dir"`
}

type ExerciseRepositoryConfig struct {
	Repository   string `json:"repository"`
	Ref          string `json:"ref"`
	Subdirectory string `json:"subdirectory"`
	CacheDir     string `json:"cache_dir"`
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
	if cfg.VMService.TimeoutSeconds == 0 {
		cfg.VMService.TimeoutSeconds = 30
	}
	if cfg.VMService.RemoteWorkDir == "" {
		cfg.VMService.RemoteWorkDir = "/var/tmp/incidentctl/exercises"
	}
	if cfg.ExerciseRepository.Ref == "" {
		cfg.ExerciseRepository.Ref = "main"
	}
	if cfg.ExerciseRepository.Subdirectory == "" {
		cfg.ExerciseRepository.Subdirectory = "exersice"
	}
	cacheDir, err := expandHome(cfg.ExerciseRepository.CacheDir)
	if err != nil {
		return cfg, err
	}
	cfg.ExerciseRepository.CacheDir = cacheDir
	if err := cfg.VMService.Validate(); err != nil {
		return cfg, fmt.Errorf("vm_service: %w", err)
	}
	if err := cfg.ExerciseRepository.Validate(); err != nil {
		return cfg, fmt.Errorf("exercise_repository: %w", err)
	}
	return cfg, nil
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

func (c MicroVMServiceConfig) Validate() error {
	parsedURL, err := url.ParseRequestURI(c.BaseURL)
	if err != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return errors.New("base_url must be an absolute HTTP or HTTPS URL")
	}
	if c.TokenEnv != "" && !safeEnvironmentName.MatchString(c.TokenEnv) {
		return errors.New("token_env must be a valid environment variable name")
	}
	if c.TimeoutSeconds < 1 {
		return errors.New("timeout_seconds must be positive")
	}
	if !safeAbsolutePath(c.RemoteWorkDir) {
		return errors.New("remote_work_dir must be an absolute path other than /")
	}
	return nil
}

func (c ExerciseRepositoryConfig) Validate() error {
	if strings.TrimSpace(c.Repository) == "" || strings.ContainsAny(c.Repository, "\r\n") {
		return errors.New("repository is required and must not contain line breaks")
	}
	if strings.TrimSpace(c.Ref) == "" || strings.ContainsAny(c.Ref, "\r\n") {
		return errors.New("ref is required and must not contain line breaks")
	}
	cleanSubdirectory := filepath.Clean(c.Subdirectory)
	if filepath.IsAbs(cleanSubdirectory) || cleanSubdirectory == ".." || strings.HasPrefix(filepath.ToSlash(cleanSubdirectory), "../") {
		return errors.New("subdirectory must be a relative path inside the repository")
	}
	if !safeAbsolutePath(c.CacheDir) {
		return errors.New("cache_dir must be an absolute path other than /")
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
