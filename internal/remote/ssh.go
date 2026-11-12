package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"incidentctl/internal/vm"
)

type Client struct {
	config         vm.Connection
	out            io.Writer
	errOut         io.Writer
	attemptCommand func(string, string) *exec.Cmd
	retrySleep     func(time.Duration)
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
	ExitCode      int
	BeforeCommand bool
	Attempts      int
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("remote command exited with code %d", e.ExitCode)
}

func IsCommandError(err error) bool {
	var commandErr *CommandError
	return errors.As(err, &commandErr)
}

func (c *Client) RunScriptOutput(script string, args ...string) (string, error) {
	return c.RunScriptOutputContext(context.Background(), script, args...)
}

// RunScriptOutputContext runs a script and returns its trimmed standard output.
// Cancelling ctx terminates the local ssh process.
func (c *Client) RunScriptOutputContext(ctx context.Context, script string, args ...string) (string, error) {
	var output bytes.Buffer
	if err := c.runContext(ctx, c.scriptCommand(args...), func() (io.Reader, error) { return strings.NewReader(script), nil }, &output); err != nil {
		return "", err
	}
	return strings.TrimSpace(output.String()), nil
}

func (c *Client) RunScript(script string, args ...string) error {
	return c.RunScriptContext(context.Background(), script, args...)
}

func (c *Client) RunScriptContext(ctx context.Context, script string, args ...string) error {
	return c.runContext(ctx, c.scriptCommand(args...), func() (io.Reader, error) { return strings.NewReader(script), nil }, c.out)
}

func (c *Client) RunShellInput(script string, input io.Reader, args ...string) error {
	return c.RunShellInputContext(context.Background(), script, input, args...)
}

func (c *Client) RunShellInputContext(ctx context.Context, script string, input io.Reader, args ...string) error {
	parts := []string{"bash", "-c", script, "--"}
	parts = append(parts, args...)
	return c.RunWithInputContext(ctx, input, parts...)
}

func (c *Client) RunWithInput(input io.Reader, command ...string) error {
	return c.RunWithInputContext(context.Background(), input, command...)
}

func (c *Client) RunWithInputContext(ctx context.Context, input io.Reader, command ...string) error {
	open, cleanup, err := replayInput(input)
	if err != nil {
		return err
	}
	defer cleanup()
	return c.runContext(ctx, joinCommand(command), open, c.out)
}

func (c *Client) scriptCommand(args ...string) string {
	parts := []string{"bash", "-s", "--"}
	parts = append(parts, args...)
	return joinCommand(parts)
}

func (c *Client) command(remoteCommand string) *exec.Cmd {
	return c.commandContext(context.Background(), remoteCommand)
}

func (c *Client) commandContext(ctx context.Context, remoteCommand string) *exec.Cmd {
	proxy, proxyErr := ProxyOption()
	args := []string{
		"-p", strconv.Itoa(c.config.Port),
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=30",
		"-o", "ConnectionAttempts=1",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=2",
	}
	if c.config.IdentityFile != "" {
		args = append(args, "-i", c.config.IdentityFile)
	}
	if proxy != "" {
		args = append(args, "-o", proxy)
		if c.config.Bastion != nil {
			proxyErr = errors.New("SSH proxy cannot be combined with a bastion")
		}
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
	cmd := exec.CommandContext(ctx, "ssh", args...)
	// Do not wait forever for pipes held by a killed ssh process.
	cmd.WaitDelay = 5 * time.Second
	if proxyErr != nil {
		cmd.Err = proxyErr
	}
	return cmd
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

// TransportError reports that ssh itself could not run or reach the host, as
// opposed to the remote command finishing with a result.
type TransportError struct{ Err error }

func (e *TransportError) Error() string { return "run ssh: " + e.Err.Error() }
func (e *TransportError) Unwrap() error { return e.Err }

// sshTransportExit is the exit status OpenSSH reserves for its own failures
// (connection, authentication, host key).
const sshTransportExit = 255

// IsTransportError is true when ssh failed before or instead of producing a
// remote command result.
func IsTransportError(err error) bool {
	var transport *TransportError
	if errors.As(err, &transport) {
		return true
	}
	var commandErr *CommandError
	return errors.As(err, &commandErr) && commandErr.ExitCode == sshTransportExit
}

func describe(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	// A cancelled context kills ssh; report the context, not "signal: killed".
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("run ssh: %w", ctxErr)
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return &CommandError{ExitCode: exitErr.ExitCode()}
	}
	return &TransportError{Err: err}
}
