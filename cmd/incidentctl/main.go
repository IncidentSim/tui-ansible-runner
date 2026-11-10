package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"incidentctl/internal/config"
	"incidentctl/internal/remote"
)

const exerciseRoot = "exersice"

type connectionInfo struct {
	Host         string  `json:"host"`
	IP           string  `json:"ip"`
	Port         int     `json:"port"`
	Login        string  `json:"login"`
	Password     *string `json:"password"`
	IdentityFile string  `json:"identity_file"`
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("incidentctl", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	configPath := flags.String("config", "incidentctl.json", "path to the local JSON configuration")
	flags.Usage = usage
	if err := flags.Parse(args); err != nil {
		return 2
	}
	rest := flags.Args()
	if len(rest) == 0 || rest[0] == "help" || rest[0] == "-h" || rest[0] == "--help" {
		usage()
		return 0
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fail(err)
	}
	client := remote.New(cfg.Target)
	command := rest[0]
	commandArgs := rest[1:]

	switch command {
	case "ssh-check":
		if len(commandArgs) != 0 {
			return fail(errors.New("ssh-check does not accept arguments"))
		}
		err = sshCheck(client, cfg)
	case "connection":
		if len(commandArgs) != 0 {
			return fail(errors.New("connection does not accept arguments"))
		}
		err = printConnection(client, cfg)
	case "infra", "incident", "verify":
		if len(commandArgs) == 0 {
			return fail(fmt.Errorf("%s requires an exercise name", command))
		}
		exercise, dir, loadErr := config.LoadExercise(exerciseRoot, commandArgs[0])
		if loadErr != nil {
			return fail(loadErr)
		}
		extra := commandArgs[1:]
		if len(extra) > 0 && extra[0] == "--" {
			extra = extra[1:]
		}
		playbook, selectErr := selectPlaybook(exercise, command)
		if selectErr != nil {
			return fail(selectErr)
		}
		err = runPlaybook(client, cfg, exercise, dir, playbook, extra)
	default:
		usage()
		return fail(fmt.Errorf("unknown command %q", command))
	}
	if err != nil {
		return fail(err)
	}
	return 0
}

func usage() {
	fmt.Fprintln(os.Stderr, `incidentctl — запуск incident exercises на готовой Linux VM

Usage:
  incidentctl [-config incidentctl.json] ssh-check
  incidentctl [-config incidentctl.json] connection
  incidentctl [-config incidentctl.json] infra    <exercise> [-- ansible-playbook args]
  incidentctl [-config incidentctl.json] incident <exercise> [-- ansible-playbook args]
  incidentctl [-config incidentctl.json] verify   <exercise> [-- ansible-playbook args]

Exercise definition: exersice/<exercise>/vm.json`)
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "error:", err)
	return 1
}

func sshCheck(client *remote.Client, cfg config.Config) error {
	return client.RunScript(sshCheckScript, cfg.Paths.RemoteWorkDir)
}

func printConnection(client *remote.Client, cfg config.Config) error {
	ip, err := client.RunScriptOutput(connectionIPScript)
	if err != nil {
		return fmt.Errorf("get target IP: %w", err)
	}
	info := connectionInfo{
		Host:         cfg.Target.Host,
		IP:           ip,
		Port:         cfg.Target.Port,
		Login:        cfg.Target.User,
		Password:     nil,
		IdentityFile: cfg.Target.IdentityFile,
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

func runPlaybook(client *remote.Client, cfg config.Config, exercise config.Exercise, dir, playbook string, extra []string) error {
	archive, err := makeArchive(dir)
	if err != nil {
		return err
	}
	if err := client.RunShellInput(uploadScript, bytes.NewReader(archive), cfg.Paths.RemoteWorkDir, exercise.Name); err != nil {
		return fmt.Errorf("upload exercise: %w", err)
	}
	args := []string{cfg.Paths.RemoteWorkDir, exercise.Name, playbook}
	args = append(args, extra...)
	return client.RunScript(playbookScript, args...)
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
