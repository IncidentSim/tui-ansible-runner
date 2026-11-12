// Package verifier runs an exercise's Ansible verification on a remote VM and
// turns its structured JSON report into a trustworthy verdict.
package verifier

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// SchemaV1 is the only supported report schema.
const SchemaV1 = "incidentsim.verify/v1"

// Hard bounds applied before and after decoding untrusted report data.
const (
	MaxReportBytes     = 1 << 20
	maxChecks          = 500
	maxDescriptionLen  = 512
	maxDetailLen       = 4096
	maxTimestampLength = 64
)

// Status is the outcome of one check or of the whole report.
type Status string

const (
	StatusPass  Status = "pass"
	StatusFail  Status = "fail"
	StatusError Status = "error"
)

var (
	checkIDPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	attemptIDPattern  = regexp.MustCompile(`^[A-Z2-7]{26}$`)
	scenarioIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)
)

// Report is the verification report written on the VM by the incidentsim.verify
// callback plugin.
type Report struct {
	Schema     string  `json:"schema"`
	Scenario   string  `json:"scenario"`
	AttemptID  string  `json:"attempt_id"`
	Complete   bool    `json:"complete"`
	Status     Status  `json:"status"`
	StartedAt  string  `json:"started_at,omitempty"`
	FinishedAt string  `json:"finished_at,omitempty"`
	Checks     []Check `json:"checks"`
}

// Check is one named observation with an explicit outcome.
type Check struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	Status      Status `json:"status"`
	Observed    string `json:"observed,omitempty"`
	Expected    string `json:"expected,omitempty"`
	Evidence    string `json:"evidence,omitempty"`
}

// Expectation is the identity and contract a report must satisfy.
type Expectation struct {
	Scenario  string
	AttemptID string
	Schema    string
	// Required lists checks that must be present when the report passes.
	Required []string
}

// InvalidReportError means the report cannot be trusted. It is always a
// technical error, never a verdict about the participant's solution.
type InvalidReportError struct{ Reason string }

func (e *InvalidReportError) Error() string { return "invalid verification report: " + e.Reason }

func invalid(format string, args ...any) error {
	return &InvalidReportError{Reason: fmt.Sprintf(format, args...)}
}

// DecodeReport strictly decodes and validates untrusted report bytes.
func DecodeReport(data []byte, want Expectation) (Report, error) {
	var report Report
	if len(data) == 0 {
		return report, invalid("empty report")
	}
	if len(data) > MaxReportBytes {
		return report, invalid("report is larger than %d bytes", MaxReportBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return Report{}, invalid("decode: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Report{}, invalid("trailing data after the JSON document")
	}
	if err := report.validate(want); err != nil {
		return Report{}, err
	}
	return report, nil
}

func (r Report) validate(want Expectation) error {
	if want.Schema == "" {
		want.Schema = SchemaV1
	}
	if r.Schema != want.Schema || r.Schema != SchemaV1 {
		return invalid("schema %q, want %q", r.Schema, want.Schema)
	}
	if !scenarioIDPattern.MatchString(r.Scenario) || r.Scenario != want.Scenario {
		return invalid("scenario %q does not match %q", r.Scenario, want.Scenario)
	}
	if !attemptIDPattern.MatchString(want.AttemptID) || r.AttemptID != want.AttemptID {
		return invalid("attempt_id does not match this verification attempt")
	}
	if !r.Complete {
		return invalid("report is marked incomplete")
	}
	if len(r.StartedAt) > maxTimestampLength || len(r.FinishedAt) > maxTimestampLength {
		return invalid("timestamps are too long")
	}
	if len(r.Checks) == 0 {
		return invalid("report contains no checks")
	}
	if len(r.Checks) > maxChecks {
		return invalid("report contains more than %d checks", maxChecks)
	}
	seen := make(map[string]Status, len(r.Checks))
	derived := StatusPass
	for _, check := range r.Checks {
		if !checkIDPattern.MatchString(check.ID) {
			return invalid("check id %q is invalid", check.ID)
		}
		if _, dup := seen[check.ID]; dup {
			return invalid("duplicate check %q", check.ID)
		}
		switch check.Status {
		case StatusPass:
		case StatusFail:
			if derived == StatusPass {
				derived = StatusFail
			}
		case StatusError:
			derived = StatusError
		default:
			return invalid("check %q has status %q", check.ID, check.Status)
		}
		seen[check.ID] = check.Status
		if err := checkText(check); err != nil {
			return err
		}
	}
	if r.Status != StatusPass && r.Status != StatusFail && r.Status != StatusError {
		return invalid("status %q is not pass, fail or error", r.Status)
	}
	if r.Status != derived {
		return invalid("status %q does not match its checks (%q)", r.Status, derived)
	}
	if derived == StatusPass {
		for _, id := range want.Required {
			if _, ok := seen[id]; !ok {
				return invalid("passing report lacks required check %q", id)
			}
		}
	}
	return nil
}

func checkText(c Check) error {
	for name, field := range map[string]struct {
		value string
		limit int
	}{
		"description": {c.Description, maxDescriptionLen},
		"observed":    {c.Observed, maxDetailLen},
		"expected":    {c.Expected, maxDetailLen},
		"evidence":    {c.Evidence, maxDetailLen},
	} {
		if len(field.value) > field.limit {
			return invalid("check %q %s is longer than %d bytes", c.ID, name, field.limit)
		}
		if strings.ContainsRune(field.value, 0) {
			return invalid("check %q %s contains NUL", c.ID, name)
		}
	}
	return nil
}

// Summary renders a short human-readable table. It prints only bounded,
// already-redacted report fields, never raw playbook output.
func (r Report) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Результат проверки %s: %s\n", r.Scenario, strings.ToUpper(string(r.Status)))
	for _, c := range r.Checks {
		fmt.Fprintf(&b, "  [%s] %s", strings.ToUpper(string(c.Status)), c.ID)
		if c.Status != StatusPass {
			if c.Expected != "" {
				fmt.Fprintf(&b, "\n      ожидалось: %s", clip(c.Expected))
			}
			if c.Observed != "" {
				fmt.Fprintf(&b, "\n      получено: %s", clip(c.Observed))
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func clip(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 240 {
		return value[:240] + "…"
	}
	return value
}
