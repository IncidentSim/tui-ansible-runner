package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ManifestFile is the optional exercise manifest next to vm.json.
const ManifestFile = "manifest.yaml"

// ReportSchemaV1 is the only structured verification report schema.
const ReportSchemaV1 = "incidentsim.verify/v1"

const (
	minVerifyTimeout = 10 * time.Second
	maxVerifyTimeout = 30 * time.Minute
	maxManifestBytes = 256 << 10
)

var safeCheckID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Verification describes how an exercise is verified. For legacy exercises
// (vm.json only) Report is nil and the playbook exit code stays authoritative.
type Verification struct {
	Playbook string
	Timeout  time.Duration
	Report   *ReportContract
}

// ReportContract declares the structured report expected from verification.
type ReportContract struct {
	Schema string
	// Checks must all be present for a passing report.
	Checks []string
}

type manifestDocument struct {
	SchemaVersion int    `yaml:"schema_version"`
	ID            string `yaml:"id"`
	Description   string `yaml:"description"`
	Environment   string `yaml:"environment"`
	Actions       struct {
		Provision manifestAction `yaml:"provision"`
		Inject    manifestAction `yaml:"inject"`
	} `yaml:"actions"`
	Verification struct {
		Playbook string `yaml:"playbook"`
		Timeout  string `yaml:"timeout"`
		Report   struct {
			Schema string   `yaml:"schema"`
			Checks []string `yaml:"checks"`
		} `yaml:"report"`
	} `yaml:"verification"`
}

type manifestAction struct {
	Playbook string `yaml:"playbook"`
}

func readManifest(path string) (manifestDocument, error) {
	var doc manifestDocument
	data, err := readBounded(path, maxManifestBytes)
	if err != nil {
		return doc, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&doc); err != nil {
		return doc, fmt.Errorf("decode %s: %w", path, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return doc, fmt.Errorf("decode %s: trailing YAML document", path)
	}
	return doc, nil
}

func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, limit)
	}
	return data, nil
}

func relativeInside(field, value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s is required", field)
	}
	clean := filepath.Clean(value)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(filepath.ToSlash(clean), "../") || strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("%s must be a relative path inside the exercise", field)
	}
	return clean, nil
}

// manifestCheck validates the manifest document and returns the verification
// contract, the environment file and the playbooks declared by the actions.
func (m manifestDocument) build(name string) (Verification, string, ExerciseActions, error) {
	var v Verification
	var actions ExerciseActions
	if m.SchemaVersion != 1 {
		return v, "", actions, fmt.Errorf("unsupported manifest schema_version %d", m.SchemaVersion)
	}
	if m.ID != name {
		return v, "", actions, fmt.Errorf("manifest id must match directory name %q", name)
	}
	if strings.TrimSpace(m.Description) == "" {
		return v, "", actions, errors.New("manifest description is required")
	}
	environment := m.Environment
	if environment == "" {
		environment = "vm.json"
	}
	environment, err := relativeInside("manifest environment", environment)
	if err != nil {
		return v, "", actions, err
	}
	for field, target := range map[string]struct {
		value string
		dest  *string
	}{
		"manifest actions.provision.playbook": {m.Actions.Provision.Playbook, &actions.Infra},
		"manifest actions.inject.playbook":    {m.Actions.Inject.Playbook, &actions.Incident},
		"manifest verification.playbook":      {m.Verification.Playbook, &actions.Verification},
	} {
		clean, err := relativeInside(field, target.value)
		if err != nil {
			return v, "", actions, err
		}
		*target.dest = filepath.ToSlash(clean)
	}
	timeout, err := time.ParseDuration(m.Verification.Timeout)
	if m.Verification.Timeout == "" {
		return v, "", actions, errors.New("manifest verification.timeout is required")
	}
	if err != nil || timeout < minVerifyTimeout || timeout > maxVerifyTimeout {
		return v, "", actions, fmt.Errorf("manifest verification.timeout must be a duration between %s and %s", minVerifyTimeout, maxVerifyTimeout)
	}
	if m.Verification.Report.Schema != ReportSchemaV1 {
		return v, "", actions, fmt.Errorf("manifest verification.report.schema must be %q", ReportSchemaV1)
	}
	if len(m.Verification.Report.Checks) == 0 {
		return v, "", actions, errors.New("manifest verification.report.checks must list at least one check")
	}
	seen := map[string]struct{}{}
	for _, id := range m.Verification.Report.Checks {
		if !safeCheckID.MatchString(id) {
			return v, "", actions, fmt.Errorf("manifest report check id %q is invalid", id)
		}
		if _, dup := seen[id]; dup {
			return v, "", actions, fmt.Errorf("manifest report contains duplicate check %q", id)
		}
		seen[id] = struct{}{}
	}
	v = Verification{
		Playbook: actions.Verification,
		Timeout:  timeout,
		Report:   &ReportContract{Schema: m.Verification.Report.Schema, Checks: append([]string(nil), m.Verification.Report.Checks...)},
	}
	return v, filepath.ToSlash(environment), actions, nil
}

// mergePlaybooks fills missing vm.json playbooks from the manifest and
// rejects disagreement, so a single source of truth cannot silently diverge.
func mergePlaybooks(vm *ExerciseActions, manifest ExerciseActions) error {
	for _, field := range []struct {
		name     string
		vm       *string
		manifest string
	}{
		{"provision/infra", &vm.Infra, manifest.Infra},
		{"inject/incident", &vm.Incident, manifest.Incident},
		{"verification", &vm.Verification, manifest.Verification},
	} {
		if *field.vm == "" {
			*field.vm = field.manifest
			continue
		}
		if filepath.Clean(*field.vm) != filepath.Clean(field.manifest) {
			return fmt.Errorf("playbook %s differs between vm.json (%s) and manifest (%s)", field.name, *field.vm, field.manifest)
		}
	}
	return nil
}
