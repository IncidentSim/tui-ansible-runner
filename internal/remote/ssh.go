package remote

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"incidentctl/internal/vm"
)

type Client struct {
	config vm.Connection
	out    io.Writer
	errOut io.Writer
}

func New(cfg vm.Connection) *Client {
	return &Client{config: cfg, out: os.Stdout, errOut: os.Stderr}
}

// NewWithWriters creates a client whose remote output can be embedded in a UI
// or captured by a caller instead of being written directly to the terminal.
func NewWithWriters(cfg vm.Connection, out, errOut io.Writer) *Client {
	return &Client{config: cfg, out: out, errOut: errOut}
}

// CommandError reports a non-zero exit status returned by the remote command.
// Transport errors (for example, failing to start ssh) use a different error.
type CommandError struct {
	ExitCode int
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("remote command exited with code %d", e.ExitCode)
}

func IsCommandError(err error) bool {
	var commandErr *CommandError
	return errors.As(err, &commandErr)
}

func (c *Client) RunScriptOutput(script string, args ...string) (string, error) {
	var output bytes.Buffer
	cmd := c.command(c.scriptCommand(args...))
	cmd.Stdin = strings.NewReader(script)
	cmd.Stdout = &output
	cmd.Stderr = c.errOut
	if err := describe(cmd.Run()); err != nil {
		return "", err
	}
	return strings.TrimSpace(output.String()), nil
}

func (c *Client) RunScript(script string, args ...string) error {
	cmd := c.command(c.scriptCommand(args...))
	cmd.Stdin = strings.NewReader(script)
	cmd.Stdout = c.out
	cmd.Stderr = c.errOut
	return describe(cmd.Run())
}

func (c *Client) RunShellInput(script string, input io.Reader, args ...string) error {
	parts := []string{"bash", "-c", script, "--"}
	parts = append(parts, args...)
	return c.RunWithInput(input, parts...)
}

func (c *Client) RunWithInput(input io.Reader, command ...string) error {
	cmd := c.command(joinCommand(command))
	cmd.Stdin = input
	cmd.Stdout = c.out
	cmd.Stderr = c.errOut
	return describe(cmd.Run())
}

func (c *Client) scriptCommand(args ...string) string {
	parts := []string{"bash", "-s", "--"}
	parts = append(parts, args...)
	return joinCommand(parts)
}

func (c *Client) command(remoteCommand string) *exec.Cmd {
	args := []string{
		"-p", strconv.Itoa(c.config.Port),
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ConnectionAttempts=3",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=2",
	}
	if c.config.IdentityFile != "" {
		args = append(args, "-i", c.config.IdentityFile)
	}
	if c.config.Bastion != nil && c.config.Bastion.Host != "" && c.config.Bastion.Port > 0 && c.config.Bastion.User != "" {
		args = append(args, "-J", fmt.Sprintf("%s@%s:%d", c.config.Bastion.User, c.config.Bastion.Host, c.config.Bastion.Port))
	}
	if c.config.StrictHostKeyChecking {
		args = append(args, "-o", "StrictHostKeyChecking=yes")
	} else {
		args = append(args, "-o", "StrictHostKeyChecking=accept-new")
	}
	args = append(args, c.config.User+"@"+c.config.Host, remoteCommand)
	return exec.Command("ssh", args...)
}

func joinCommand(parts []string) string {
	quoted := make([]string, len(parts))
	for i, part := range parts {
		quoted[i] = shellQuote(part)
	}
	return strings.Join(quoted, " ")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func describe(err error) error {
	if err == nil {
		return nil
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return &CommandError{ExitCode: exitErr.ExitCode()}
	}
	return fmt.Errorf("run ssh: %w", err)
}
