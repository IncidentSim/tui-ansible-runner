package verifier

import (
	"encoding/json"
	"strings"
	"testing"
)

const testAttempt = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"

func testExpect() Expectation {
	return Expectation{Scenario: "nginx-wrong-api", AttemptID: "ABCDEFGHIJKLMNOPQRSTUVWXYZ", Required: []string{"a", "b"}}
}

func goodReport() Report {
	return Report{
		Schema: SchemaV1, Scenario: "nginx-wrong-api", AttemptID: testAttempt, Complete: true, Status: StatusPass,
		Checks: []Check{{ID: "a", Status: StatusPass}, {ID: "b", Status: StatusPass, Observed: "ok"}},
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDecodeReportAcceptsValid(t *testing.T) {
	t.Parallel()
	report, err := DecodeReport(mustJSON(t, goodReport()), testExpect())
	if err != nil || report.Status != StatusPass {
		t.Fatalf("%+v %v", report, err)
	}
}

func TestDecodeReportRejectsUntrustworthyReports(t *testing.T) {
	t.Parallel()
	mutate := func(f func(*Report)) []byte {
		r := goodReport()
		f(&r)
		return mustJSON(t, r)
	}
	cases := map[string][]byte{
		"empty":               nil,
		"not json":            []byte("<html>"),
		"trailing":            append(mustJSON(t, goodReport()), []byte(` {}`)...),
		"unknown field":       []byte(strings.Replace(string(mustJSON(t, goodReport())), `"complete"`, `"extra":1,"complete"`, 1)),
		"wrong schema":        mutate(func(r *Report) { r.Schema = "x/v2" }),
		"wrong scenario":      mutate(func(r *Report) { r.Scenario = "other" }),
		"stale attempt":       mutate(func(r *Report) { r.AttemptID = "ZZZZZZZZZZZZZZZZZZZZZZZZZZ" }),
		"incomplete":          mutate(func(r *Report) { r.Complete = false }),
		"no checks":           mutate(func(r *Report) { r.Checks = nil }),
		"missing required":    mutate(func(r *Report) { r.Checks = r.Checks[:1] }),
		"duplicate":           mutate(func(r *Report) { r.Checks[1].ID = "a" }),
		"bad id":              mutate(func(r *Report) { r.Checks[0].ID = "../a" }),
		"bad check status":    mutate(func(r *Report) { r.Checks[0].Status = "skipped" }),
		"pass with fail":      mutate(func(r *Report) { r.Checks[0].Status = StatusFail }),
		"pass with error":     mutate(func(r *Report) { r.Checks[0].Status = StatusError }),
		"fail masks error":    mutate(func(r *Report) { r.Status = StatusFail; r.Checks[0].Status = StatusError }),
		"unknown top status":  mutate(func(r *Report) { r.Status = "ok" }),
		"oversized detail":    mutate(func(r *Report) { r.Checks[0].Evidence = strings.Repeat("x", maxDetailLen+1) }),
		"huge":                []byte(strings.Repeat(" ", MaxReportBytes+1)),
		"bool as string":      []byte(strings.Replace(string(mustJSON(t, goodReport())), `"complete":true`, `"complete":"true"`, 1)),
		"null checks element": []byte(strings.Replace(string(mustJSON(t, goodReport())), `"checks":[`, `"checks":[null,`, 1)),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := DecodeReport(data, testExpect())
			if err == nil {
				t.Fatal("report must be rejected")
			}
			if _, ok := err.(*InvalidReportError); !ok {
				t.Fatalf("want *InvalidReportError, got %T %v", err, err)
			}
		})
	}
}

func TestDecodeReportFailAllowsMissingRequiredChecks(t *testing.T) {
	t.Parallel()
	r := goodReport()
	r.Status = StatusFail
	r.Checks = []Check{{ID: "a", Status: StatusFail, Observed: "404", Expected: "200"}}
	report, err := DecodeReport(mustJSON(t, r), testExpect())
	if err != nil || report.Status != StatusFail {
		t.Fatalf("an early explicit failure is a valid report: %v", err)
	}
}

func TestDecodeReportErrorStatusIsDerivedFromChecks(t *testing.T) {
	t.Parallel()
	r := goodReport()
	r.Status = StatusError
	r.Checks[1].Status = StatusError
	if _, err := DecodeReport(mustJSON(t, r), testExpect()); err != nil {
		t.Fatal(err)
	}
}

func TestSummaryIsBounded(t *testing.T) {
	t.Parallel()
	r := goodReport()
	r.Status = StatusFail
	r.Checks[0] = Check{ID: "a", Status: StatusFail, Observed: strings.Repeat("o", 1000), Expected: "200"}
	summary := r.Summary()
	if strings.Count(summary, "o") > 300 || !strings.Contains(summary, "[FAIL] a") {
		t.Fatal(summary)
	}
}
