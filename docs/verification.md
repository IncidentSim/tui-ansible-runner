# Verification contract

Architecture decision: [adr/0001-ansible-verifier.md](adr/0001-ansible-verifier.md).

## Exercise layout

`exersice/<name>/manifest.yaml` (preferred) next to `vm.json`; `exersice/collections/`
holds the `incidentsim.verify` Ansible collection. Exercises that only have
`vm.json` are legacy: their verification playbook exit code stays authoritative
(exit 2 = rejected, anything else = technical error).

## Run of a manifest verification

1. `audit stop` and packet validation (unchanged; failure blocks verification).
2. New attempt ID; fresh `<work>/.verify/<exercise>/<attempt>/` (plain `mkdir`, so
   it can never pre-exist). Only the verification directory and the collection are
   uploaded; the exercise workspace and infrastructure are never touched.
3. `timeout <manifest timeout> ansible-playbook` with `ANSIBLE_COLLECTIONS_PATH`,
   `ANSIBLE_CALLBACKS_ENABLED=incidentsim.verify.report`, `INCIDENTSIM_SCENARIO`,
   `INCIDENTSIM_ATTEMPT_ID`, `INCIDENTSIM_REPORT_PATH`, `INCIDENTSIM_REPORT_SCHEMA`.
   The local ssh process is also bound to a context deadline.
4. The report is fetched (max 1 MiB) and strictly validated (schema, scenario,
   attempt, complete, unique known checks, status derived from checks, required
   checks present on pass, bounded text).
5. Audit recording resumes immediately, before any LLM call.
6. The raw report is saved atomically as `verification-report.json` next to
   `llm.json`, and embedded in `validation.json` (`status`, `report`) for the postmortem.

| Outcome | Meaning |
| --- | --- |
| report `pass` | solution accepted |
| report `fail` | explicit failed check: rejected / incident reproduced |
| report `error`, missing/invalid/stale/oversized report, timeout, SSH failure (exit 255 or context error), audit resume failure | technical error, never a verdict |

The TUI's "incident reproduced" check after injection uses the same engine and
accepts only a validated `fail` report.

## Testing and limits

* Go: `go test -race ./...` (SSH is mocked at `verifier.Remote`; the scripts run in a local shell).
* `internal/verifier/integration_test.go` runs real `ansible-playbook`, the real
  nginx exercise and collection against a local HTTP fixture (defaults to the
  `exersice` submodule and `../test-venv`; override with `INCIDENTSIM_EXERCISES`
  and `INCIDENTSIM_ANSIBLE_BIN`).
* Exercise repository: `pytest collections tests` (fixtures and `runuser` shims, no VM).
* **Not tested:** real VMs, real PostgreSQL/Nginx, the full
  `infra → verify → incident → verify → infra → verify` lifecycle, the real
  `audit-agent` analysis.
