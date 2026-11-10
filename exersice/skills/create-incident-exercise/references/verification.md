# Black-box verification

Verification represents the user's success criterion, not the implementation chosen to satisfy it.

Prefer protocol-level checks such as HTTP responses, DNS answers, database queries through the public interface, or service API behavior. Use controlled inputs and exact assertions. Do not check private file contents, process IDs, container IDs, or a particular remediation command unless the exercise contract explicitly exposes them.

The verification playbook:

- targets `localhost` with a local connection;
- does not mutate the infrastructure;
- returns zero only when the observable contract is satisfied;
- produces a failure message containing expected and actual values;
- remains deterministic across repeated runs;
- distinguishes an unreachable dependency from a wrong result when that distinction helps diagnosis.

Prove all three invariants:

1. Verification passes after the infra playbook.
2. Verification fails after the incident for the intended assertion.
3. Verification passes after the infra playbook restores the baseline.
