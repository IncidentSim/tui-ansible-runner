# ADR 0001: Ansible is the only scenario verification engine

Status: accepted

## Context

`incidentctl` provisions a VM, injects an incident, records the participant's
actions (audit-agent) and, on request, runs the exercise verification playbook.
Today the result of verification is the process exit code of
`ansible-playbook`, and the TUI treats exit code 2 as "solution rejected".
Exit code 2 also covers every technical failure of any task (missing binary,
templating error, tool crash), so a broken verifier is indistinguishable from a
participant who did not fix the incident. The same ambiguity affects the
"does the incident really reproduce" check run after injection.

The reference project (`incidentsim`, read only) contains a Go verifier runtime
with useful semantics (metric windows, log scopes, aggregates) and some flaws
(its exec check ignores the exit code, its sampler and log readers can return
an empty success). We reuse the ideas, not the code.

## Decision

1. **Ansible is the only scenario execution engine.** There is no second Go
   verifier runtime. Go code only orchestrates: it ships a bundle, runs
   `ansible-playbook`, fetches and validates a report.
2. **Manifest.** Every exercise has `manifest.yaml` with `id`, `description`,
   `environment` (the `vm.json`), `actions.provision`, `actions.inject`,
   `verification.{playbook,timeout,report}`. A legacy exercise that only has
   `vm.json` keeps working unchanged (its `playbooks.*` fields stay and the
   verification keeps the exit-code contract). The catalog scan supports both.
   When both exist, the manifest is authoritative and any disagreement with
   `vm.json` playbooks is a load error.
3. **Structured report.** Manifest-driven verification playbooks produce one
   JSON report (`incidentsim.verify/v1`) containing `scenario`, `attempt_id`,
   `complete`, an overall `status` and per-check
   `{id, description, status: pass|fail|error, observed, expected, evidence}`.
   Only an explicit `fail` check is evidence of an unsolved/reproduced
   incident. `error` and every transport, upload, timeout, missing, oversized,
   malformed, mismatched or incomplete report outcome is a technical error and
   is never reported as a rejected solution or as a proof of the incident.
4. **Collection `incidentsim.verify`** (in the exercise repository) separates
   machinery from composition. Modules/filters/callback hold the machinery
   (`check`, `metrics`, `logs`, `dig`, `technical_error`, report callback).
   Playbooks only compose standard `uri`/`command`/`assert`-style observations
   and checks. The callback records every technical task failure as an `error`
   check, so a crashed task cannot silently become a pass, and writes the report
   atomically when the playbook ends. No play-wide `failed_when: false` or
   `ignore_errors`.
5. **Runner contract** (`internal/verifier`, used by CLI and TUI alike):
   - create an unguessable attempt ID and a fresh remote directory
     `<work>/.verify/<exercise>/<attempt>/`; the directory must not pre-exist, so
     a stale report can never be accepted;
   - verification never deletes or re-extracts the exercise workspace and never
     provisions infrastructure; only the verification directory and the
     collection are uploaded into the attempt directory;
   - pause audit (`stop`) and validate the audit packet **before** running the
     verifier, as today; failure to stop/validate blocks verification;
   - run the verifier with explicit inputs (scenario, attempt, report path,
     schema) and a timeout enforced remotely (`timeout`) and locally
     (context cancellation of the SSH process);
   - fetch the report with a hard size bound, decode it strictly (unknown
     fields, trailing data, wrong schema/scenario/attempt, incomplete, unknown
     or duplicate checks, status not matching checks are rejected);
   - resume audit recording immediately after the verifier and before any LLM
     call; a failed resume is reported as a technical error, never as a rejected
     solution; persist the report next to the audit packet (atomic write) so the
     postmortem sees it;
   - distinguish SSH/transport failure (exit 255, context errors) from a
     negative test result (explicit failed check).
6. **Initial broken-state check** (after incident injection) uses the same
   engine: it succeeds only when the report validates and its status is `fail`.
7. **Distribution.** The collection travels in the verification bundle and is
   enabled for the run through `ANSIBLE_COLLECTIONS_PATH` and
   `ANSIBLE_CALLBACKS_ENABLED=incidentsim.verify.report` (plus the explicit
   `INCIDENTSIM_*` inputs).
8. **Secrets.** Reports are redacted by the collection and size-bounded by the
   runner; password-handling tasks keep `no_log`.

## Out of scope / deferred

- External Go replay loader (optional, later).
- Hidden-code sandbox (later).
- Real infrastructure runs: tests use isolated local HTTP and Ansible fixtures
  and a mocked remote boundary. Lifecycle `infra → verify → incident → verify →
  infra → verify` on a VM is still required before release.

## Consequences

- One verification language for authors; reports are comparable across
  scenarios and consumable by the postmortem.
- A new Python collection must be versioned together with the exercise repository
  (the runner pins it through the `exersice` gitlink).
- Playbook exit code stays meaningful (a failed check fails the task) but the
  runner trusts only the validated report.
