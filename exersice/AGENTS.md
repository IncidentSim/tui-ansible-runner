# Rules for incident exercises

These rules apply to every exercise under `exersice/`.

Use the project-local `$create-incident-exercise` skill when creating or substantially changing an exercise. Read only the skill references relevant to the part being changed.

## Required contract

- Keep the entry points declared in `vm.json`: `infra`, `incident`, and `verification`.
- Put application sources and their build files under `apps/`; do not hide application source in an Ansible template.
- Make the infra playbook idempotently produce the healthy baseline and restore it after the incident.
- Make the incident playbook idempotently introduce only the intended fault. It must fail clearly when its prerequisite infrastructure is absent.
- Make verification black-box and deterministic. It must pass after `infra`, fail after `incident`, and pass again after `infra` restores the baseline.
- Keep monitoring configuration under `infra/monitoring/` and deploy it from the infra playbook.
- Record the target OS plus exact observed bootstrap and managed package versions in `vm.json`. Keep managed package versions synchronized with the infra playbook.
- Do not put SSH credentials, passwords, private keys, or host-specific connection settings in an exercise.

## Authoring boundaries

- Describe the symptom, topology, task, and acceptance criterion in `README.md`; do not reveal the final fix there.
- When a solution is requested, put it in a separate `SOLUTION.md` with reproduction, diagnosis, root cause, remediation, and verification. Keep the participant-facing `README.md` spoiler-free.
- Order tips from investigation guidance to implementation-specific hints.
- Prefer built-in Ansible modules. Commands are acceptable when no built-in module can perform the operation without adding a collection; state `changed_when` and `failed_when` deliberately.
- Use fully qualified Ansible module names.
- Pin container runtime images to an intentional version and keep applications small enough to build during an exercise run.
- Do not make verification depend on private implementation details such as exact config-file text, process IDs, or container IDs.

## Definition of done

Validate JSON, run `ansible-playbook --syntax-check` for all three entry points, and exercise the full lifecycle against a disposable VM:

```text
infra -> verify PASS -> incident -> verify FAIL -> infra -> verify PASS
```

Report each stage separately. A deliberately failing verification after `incident` is an expected result, not a skipped test.
