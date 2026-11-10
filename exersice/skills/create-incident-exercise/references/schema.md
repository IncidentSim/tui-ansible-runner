# Exercise schema

Each exercise is stored at `exersice/<name>/`, where `<name>` is a stable slug and matches `vm.json.name`.

Required files and directories:

```text
README.md
vm.json
apps/
infra/playbook.yml
infra/monitoring/
incident/playbook.yml
verification/playbook.yml
tips/
```

`vm.json` uses schema version 1 and contains:

- `name` matching the directory;
- informational `resources.vcpu_count` and `resources.memory_mib`;
- target `runtime.os.id` and `runtime.os.version`;
- exact observed versions in `runtime.bootstrap_packages` and `runtime.managed_packages`;
- relative entry points in `playbooks.infra`, `playbooks.incident`, and `playbooks.verification`.

Bootstrap packages are required before incidentctl can run, such as Ansible, Python, and tar. Managed packages are installed by the infra playbook, such as Nginx or Docker. Do not put connection details or secrets in `vm.json`.

The README states the symptom, topology, investigation task, command to verify the solution, and externally observable success criterion. It must not disclose the root cause or exact fix.

When the exercise includes an author solution, store it separately in `SOLUTION.md`. Explain how to reproduce the intended failure, identify its root cause from observable evidence, repair it manually or through the healthy infra source of truth, and run the black-box verification. Do not link the solution from the participant-facing README unless explicitly requested.
