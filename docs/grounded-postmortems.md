# Grounded postmortems: build and interactive check

## Scope

Ansible is still the exercise verifier. The postmortem does not change the
verdict. Empty `actions` (including context-only packets) produce a deterministic
report without a provider request. Nonempty actions keep the existing Analyze
API and local provider path. Program-rendered status, checks and action history
precede the explicitly labelled model explanation. Unknown explicit
`[action:ID]` / `[context:ID]` references reject the explanation, not the Ansible
verdict. Free-form claims and causal interpretations are not machine-validated.

The configured prompt remains data-sensitive: do not supply secrets in recorded
commands or validation evidence. Redaction of arbitrary secret values is not
promised. Noninteractive SSH telemetry is context, not confirmed participant
history; open a fresh interactive Bash login to exercise command recording.

## Pinned build (no service installation)

Use Go 1.26.0 and the exact recorded submodule commits. These integration commits
are local until the repository owner chooses to publish them. Obtain all three
repositories/patches, then initialize the submodules from those local repositories
when the commits are not available from their public remotes.

```sh
git submodule update --init --recursive
export GOTOOLCHAIN=go1.26.0
# REQUIRED for this unpublished/pinned release: default generation otherwise
# fetches the current remote audit-agent main rather than the local gitlink.
export AUDIT_AGENT_DIR="$PWD/audit-agent"
make build test vet
(cd audit-agent && go test -race ./... && go vet ./...)
```

The generator embeds the Linux guest agent and the local-platform analyzer in
`bin/incidentctl`. Generated executables are ignored build products. Preserve the
build log and executable SHA256, and test packaged analysis, not only a separately
built agent. A dirty source tree changes Go VCS build metadata; do not compare it
with a clean tree as a byte-identical reproducibility claim.

For a local patch series, apply the exercise and audit-agent patches in their own
repositories at the bases recorded in the artifact manifest; apply the runner
series in the runner repository. A gitlink alone does not transfer unpublished
submodule commits. Do not publish or deploy these commits merely to build them.

## Existing interactive TUI

1. Obtain a current personal token privately through the supported bot `/token`
   flow and configure the existing API/exercise source as usual.
2. Use the built `bin/incidentctl` and your existing `incidentctl.json`.
3. Enable `audit.llm` using your provider's protected local key/environment, or
   supply the existing standalone profile interface:

```sh
bin/incidentctl -config /path/to/incidentctl.json \
  -audit-llm-config /private/path/to/profile.json tui
```

The explicit flag selects that standalone profile instead of inline LLM settings.
The profile can reference a mode-0600 `api_key_file`; never put the key in command
arguments or chat. Use the maintained postmortem prompt, HTTPS provider URL and a
bounded request timeout. LLM requests run locally, not in the VM.

Open the exercise terminal, execute real commands in interactive Bash, then press
`v`. Recording resumes before analysis. The normal path analyzes only a passing
verification. A provider/reference failure warns and preserves the Ansible
verdict; it does not mark a solved exercise unsolved. Inspect both the program
facts and the labelled explanation, not just PASS/FAIL.

## Analyze a preserved real packet without creating another VM

Use the packaged Linux executable from
`internal/auditbinary/binaries/linux-amd64` (or a copy with executable permission):

```sh
incident-audit analyze --config /private/path/to/profile.json \
  --input /path/to/attempt/llm.json \
  --task /path/to/exercise/README.md \
  --validation /path/to/attempt/validation.json \
  --output /path/to/new-analysis-directory
```

The output directory must not exist. `analysis.json.analysis` and `analysis.md`
contain the same rendered document (apart from one file-terminating newline).
For empty actions, expect `llm_skipped: true` and no response/model/usage claiming
that a provider was called. For nonempty actions, inspect the real response ID,
model and usage, and the packet hash/session. A failed-packet standalone replay
is diagnostic, not evidence that the normal TUI analyzes failed verification.

## Rollback

Retain the prior local runner executable and its exact source revisions. Stop
only your own client, switch back to that executable/configuration and rebuild
from its recorded commits if needed. No host daemon restart or shared deployment
is required for this local release. Delete disposable guests only through the
supported owner-scoped API, retaining the exact operation and original VMM exit
receipt. Never synthesize a receipt or clear authoritative VM records manually.
