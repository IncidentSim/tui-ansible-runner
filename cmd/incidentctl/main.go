package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"incidentctl/internal/config"
	"incidentctl/internal/remote"
	"incidentctl/internal/sshproxy"
	"incidentctl/internal/vm"
)

type connectionInfo struct {
	Host         string  `json:"host"`
	IP           string  `json:"ip"`
	Port         int     `json:"port"`
	Login        string  `json:"login"`
	Password     *string `json:"password"`
	IdentityFile string  `json:"identity_file"`
}

type playbookRunError struct {
	Phase string
	Err   error
}

func (e *playbookRunError) Error() string { return e.Phase + ": " + e.Err.Error() }
func (e *playbookRunError) Unwrap() error { return e.Err }

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) > 0 && args[0] == "ssh-proxy" {
		return runSSHProxy(args[1:])
	}
	flags := flag.NewFlagSet("incidentctl", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	configPath := flags.String("config", "incidentctl.json", "path to the unified incidentctl configuration")
	vmID := flags.String("vm-id", "", "existing VM ID for non-interactive commands")
	showVersion := flags.Bool("version", false, "print the TUI and bundled audit-agent versions")
	audit := defaultAuditOptions()
	flags.StringVar(&audit.AgentDir, "audit-agent-dir", "", "optional audit-agent checkout with prebuilt bin/OS-ARCH executables; otherwise use packaged binaries")
	flags.StringVar(&audit.Binary, "audit-agent-binary", "", "optional standalone local Go audit-agent executable for LLM analysis")
	flags.StringVar(&audit.LLMConfig, "audit-llm-config", "", "local LLM configuration; empty saves logs without analysis")
	flags.StringVar(&audit.ReportsDir, "audit-reports-dir", audit.ReportsDir, "local audit reports directory")
	flags.Usage = usage
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *showVersion {
		fmt.Fprint(os.Stdout, versionText())
		return 0
	}
	rest := flags.Args()
	if len(rest) > 0 && (rest[0] == "help" || rest[0] == "-h" || rest[0] == "--help") {
		usage()
		return 0
	}
	if _, err := remote.ProxyOption(); err != nil {
		return fail(err)
	}

	cfg, configErr := config.Load(*configPath)
	if configErr != nil {
		return fail(configErr)
	}
	audit.LLMReportsDir = cfg.Audit.LLMReportsDir
	llmConfigFlagSet := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "audit-llm-config" {
			llmConfigFlagSet = true
		}
	})
	if !llmConfigFlagSet {
		audit.LLMConfig = cfg.Audit.LLMConfig
		audit.LLM = cfg.Audit.LLM
	}
	source, provider, remoteWorkDir, tokenFile, providerErr := buildRuntimeConfig(cfg)
	if len(rest) == 0 || rest[0] == "tui" {
		if providerErr != nil {
			return fail(providerErr)
		}
		if len(rest) > 1 {
			return fail(errors.New("tui does not accept arguments"))
		}
		if err := runTUI(source, provider, remoteWorkDir, tokenFile, audit); err != nil {
			return fail(err)
		}
		return 0
	}
	if providerErr != nil {
		return fail(providerErr)
	}
	ctx := context.Background()
	command := rest[0]
	commandArgs := rest[1:]
	var err error
	var commandConnection vm.Connection
	switch command {
	case "ssh-check", "connection", "infra", "incident", "verify", "record":
		if *vmID == "" {
			return fail(errors.New("non-interactive commands require -vm-id <existing VM ID>; create a VM in the TUI first"))
		}
		apiProvider, ok := provider.(vm.APIProvider)
		if !ok {
			return fail(errors.New("VM provider does not support SSH metadata"))
		}
		var cleanup func()
		commandConnection, cleanup, err = apiProvider.SSHConnection(ctx, *vmID)
		if err != nil {
			return fail(err)
		}
		if cleanup != nil {
			defer cleanup()
		}
	}

	switch command {
	case "ssh-check":
		if len(commandArgs) != 0 {
			return fail(errors.New("ssh-check does not accept arguments"))
		}
		connection := commandConnection
		err = sshCheck(remote.New(connection), remoteWorkDir)
	case "connection":
		if len(commandArgs) != 0 {
			return fail(errors.New("connection does not accept arguments"))
		}
		connection := commandConnection
		err = printConnection(remote.New(connection), connection)
	case "infra", "incident", "verify", "record":
		if len(commandArgs) == 0 {
			return fail(fmt.Errorf("%s requires an exercise name", command))
		}
		item, loadErr := source.Get(ctx, commandArgs[0])
		if loadErr != nil {
			return fail(loadErr)
		}
		connection := commandConnection
		extra := commandArgs[1:]
		if len(extra) > 0 && extra[0] == "--" {
			extra = extra[1:]
		}
		if command == "record" {
			if len(extra) != 0 {
				return fail(errors.New("record does not accept Ansible arguments"))
			}
			err = audit.start(remote.New(connection), remoteWorkDir, item.Exercise.Name)
			break
		}
		playbook, selectErr := selectPlaybook(item.Exercise, command)
		if selectErr != nil {
			return fail(selectErr)
		}
		client := remote.New(connection)
		if command == "verify" {
			err = audit.verify(client, remoteWorkDir, item.Exercise.Name, item.Dir, os.Stdout, func(out io.Writer) error {
				return runPlaybook(remote.NewWithWriters(connection, out, out), remoteWorkDir, item.Exercise, item.Dir, playbook, extra)
			})
		} else if err = audit.prepare(client, remoteWorkDir, item.Exercise.Name); err == nil {
			if err = preparePlaybookHost(client, item.Exercise); err == nil {
				err = runPlaybook(client, remoteWorkDir, item.Exercise, item.Dir, playbook, extra)
				if err == nil {
					err = audit.start(client, remoteWorkDir, item.Exercise.Name)
				}
			}
		}
	default:
		usage()
		return fail(fmt.Errorf("unknown command %q", command))
	}
	if err != nil {
		return fail(err)
	}
	return 0
}

func runSSHProxy(args []string) int {
	flags := flag.NewFlagSet("ssh-proxy", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	proxy := flags.String("url", "", "HTTP CONNECT or SOCKS5 proxy URL")
	host := flags.String("host", "", "SSH destination")
	port := flags.Int("port", 0, "SSH destination port")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		return fail(errors.New("unexpected ssh-proxy arguments"))
	}
	if err := sshproxy.Relay(context.Background(), *proxy, *host, *port, os.Stdin, os.Stdout); err != nil {
		return fail(err)
	}
	return 0
}

func usage() {
	fmt.Fprintln(os.Stderr, `incidentctl — запуск incident exercises через VM service API

Usage:
  incidentctl [config flags] [tui]
  incidentctl [config flags] ssh-check
  incidentctl [config flags] connection
  incidentctl [config flags] infra    <exercise> [-- ansible-playbook args]
  incidentctl [config flags] incident <exercise> [-- ansible-playbook args]
  incidentctl [config flags] verify   <exercise> [-- ansible-playbook args]
  incidentctl [config flags] record   <exercise>

Configuration flags:
  --version
  -config incidentctl.json
  -vm-id <existing VM ID> (required outside TUI)
  -audit-agent-dir /optional/local/audit-agent (prebuilt bin/OS-ARCH overrides)
  -audit-agent-binary /path/to/incident-audit
  -audit-llm-config /path/to/llm.local.json
  -audit-reports-dir analysis

Exercise definition: configured remote Git repository/<exercise>/vm.json`)
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "error:", err)
	return 1
}

func sshCheck(client *remote.Client, remoteWorkDir string) error {
	return client.RunScript(sshCheckScript, remoteWorkDir)
}

func printConnection(client *remote.Client, connection vm.Connection) error {
	ip := connection.IP
	if ip == "" {
		var err error
		ip, err = client.RunScriptOutput(connectionIPScript)
		if err != nil {
			return fmt.Errorf("get target IP: %w", err)
		}
	}
	info := connectionInfo{
		Host:         connection.Host,
		IP:           ip,
		Port:         connection.Port,
		Login:        connection.User,
		Password:     connection.Password,
		IdentityFile: connection.IdentityFile,
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func selectPlaybook(exercise config.Exercise, command string) (string, error) {
	switch command {
	case "infra":
		return exercise.Playbooks.Infra, nil
	case "incident":
		return exercise.Playbooks.Incident, nil
	case "verify":
		return exercise.Playbooks.Verification, nil
	default:
		return "", fmt.Errorf("command %q has no playbook", command)
	}
}

func runPlaybook(client *remote.Client, remoteWorkDir string, exercise config.Exercise, dir, playbook string, extra []string) error {
	archive, err := makeArchive(dir)
	if err != nil {
		return err
	}
	if err := client.RunShellInput(uploadScript, bytes.NewReader(archive), remoteWorkDir, exercise.Name); err != nil {
		return &playbookRunError{Phase: "upload exercise", Err: err}
	}
	args := []string{remoteWorkDir, exercise.Name, playbook}
	args = append(args, extra...)
	if err := client.RunScript(playbookScript, args...); err != nil {
		return &playbookRunError{Phase: "execute playbook", Err: err}
	}
	return nil
}

func preparePlaybookHost(client *remote.Client, exercise config.Exercise) error {
	packages := make([]string, 0, len(exercise.Runtime.BootstrapPackages)+1)
	seen := make(map[string]struct{}, len(exercise.Runtime.BootstrapPackages)+1)
	for _, pkg := range exercise.Runtime.BootstrapPackages {
		if _, exists := seen[pkg.Name]; exists {
			continue
		}
		seen[pkg.Name] = struct{}{}
		packages = append(packages, pkg.Name)
	}
	if _, exists := seen["ansible-core"]; !exists {
		packages = append(packages, "ansible-core")
	}
	return client.RunScript(bootstrapScript, packages...)
}

func makeArchive(root string) ([]byte, error) {
	var output bytes.Buffer
	w := tar.NewWriter(&output)
	err := filepath.Walk(root, func(filePath string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, filePath)
		if err != nil || rel == "." {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks are not supported in exercises: %s", filePath)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported file type: %s", filePath)
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		if err := w.WriteHeader(header); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(filePath)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(w, f)
			closeErr := f.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

const sshCheckScript = `set -euo pipefail
work_root="$1"
failed=0
check_command() {
  if command -v "$1" >/dev/null 2>&1; then
    printf 'ok   command %s (%s)\n' "$1" "$(command -v "$1")"
  else
    printf 'FAIL command %s\n' "$1"
    failed=1
  fi
}
printf 'ok   SSH connection to %s@%s\n' "$(id -un)" "$(hostname)"
if [ -r /etc/os-release ]; then
  . /etc/os-release
  printf 'ok   operating system %s\n' "$PRETTY_NAME"
else
  printf 'FAIL readable /etc/os-release\n'
  failed=1
fi
check_command bash
check_command python3
check_command ansible-playbook
check_command tar
mkdir -p "$work_root"
if [ -w "$work_root" ]; then
  printf 'ok   writable %s\n' "$work_root"
else
  printf 'FAIL writable %s\n' "$work_root"
  failed=1
fi
exit "$failed"
`

const connectionIPScript = `set -euo pipefail
ip_address="$(hostname -I | awk '{ for (i = 1; i <= NF; i++) if ($i ~ /^[0-9]+\./) { print $i; exit } }')"
test -n "$ip_address"
printf '%s\n' "$ip_address"
`

const bootstrapScript = `set -euo pipefail
missing=()
for package in "$@"; do
  if ! dpkg-query -W -f='${Status}' "$package" 2>/dev/null | grep -q '^install ok installed$'; then
    missing+=("$package")
  fi
done
if ((${#missing[@]} > 0)); then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  apt-get install -y --no-install-recommends "${missing[@]}"
fi
command -v ansible-playbook
`

const uploadScript = `set -euo pipefail
work_root="$1"; name="$2"; target="$work_root/$name"
mkdir -p "$work_root"
rm -rf -- "$target"
mkdir -p "$target"
tar -xf - -C "$target" --no-same-owner --no-same-permissions
`

const playbookScript = `set -euo pipefail
work_root="$1"; name="$2"; playbook="$3"; shift 3
target="$work_root/$name"
test -f "$target/$playbook"
cd "$target"
export ANSIBLE_NOCOWS=1
export ANSIBLE_HOST_KEY_CHECKING=True
ansible-playbook --inventory 'localhost,' --connection local "$playbook" "$@"
`
