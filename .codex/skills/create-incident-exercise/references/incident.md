# Incident playbook

An incident is one narrow, intentional difference from the healthy baseline.

The playbook must:

- target `localhost` with a local connection;
- verify that the healthy infrastructure prerequisite exists;
- change only resources relevant to the intended fault;
- be safe to run repeatedly;
- validate changed configuration before reloading a service;
- preserve SSH, Ansible, and the recovery path;
- avoid random timing, broad process kills, package removal, and unrelated cleanup unless those are the explicit learning objective.

Choose a fault that is visible through the exercise's public contract. Do not make verification fail merely because the verifier searches for a modified line of configuration.

After implementation, inspect the actual response or state produced by the fault. A nonzero verifier exit caused by a syntax error, unavailable VM, or broken deployment is not proof of the intended incident.
