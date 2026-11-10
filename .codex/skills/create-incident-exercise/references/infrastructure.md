# Healthy infrastructure

The infra playbook owns the complete healthy baseline. A repeated run must converge, and a run after the incident must restore the baseline without recreating the VM.

Keep buildable application source under `apps/<app>/`. Containerized applications include their Dockerfile beside the source. Avoid dependencies when the exercise does not need them.

In the playbook:

- target `localhost` with a local connection because Ansible runs on the exercise VM;
- assert the supported OS before installing packages;
- install managed packages at versions recorded in `vm.json`;
- use fully qualified built-in module names;
- use handlers for service restarts and configuration reloads;
- validate service configuration before reload when the service provides a validator;
- wait for application readiness before completing;
- end with a small smoke test of the healthy user path;
- deploy monitoring files from `infra/monitoring/` and avoid exposing administrative endpoints publicly.

Commands may replace a missing Ansible collection, but guard them with explicit conditions and meaningful change reporting. Do not install Ansible from the playbook that requires Ansible to run.
