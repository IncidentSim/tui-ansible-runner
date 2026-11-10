---
name: create-incident-exercise
description: Create or substantially update an IncidentSim exercise with application sources, VM requirements, Ansible infrastructure and incident playbooks, monitoring configuration, tips, and black-box verification. Use for work under exersice/; do not use for changing the incidentctl runtime alone.
---

# Create an IncidentSim exercise

Build an exercise whose healthy and broken states are reproducible on a disposable Linux VM.

Before editing, inspect the target exercise and `exersice/AGENTS.md`. Preserve user-written scenario content unless the requested behavior requires changing it.

Read the references needed for the requested scope:

- For directory layout and `vm.json`, read [references/schema.md](references/schema.md).
- For applications, monitoring, and the healthy baseline, read [references/infrastructure.md](references/infrastructure.md).
- For fault injection, read [references/incident.md](references/incident.md).
- For acceptance checks, read [references/verification.md](references/verification.md).

For a complete new exercise, read all four references and implement in this order: observable contract, application, healthy infrastructure, incident, verification, then progressive tips. Keep the exercise independent of workstation SSH credentials.

Finish with static validation and, when an authorized disposable VM is available, prove this lifecycle:

```text
infra -> verify PASS -> incident -> verify FAIL -> infra -> verify PASS
```

Treat the failure after `incident` as expected only when it fails for the intended observable symptom. Stop if the incident damages SSH access or another control-plane capability needed to recover the VM.
