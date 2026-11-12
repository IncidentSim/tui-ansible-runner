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
	var output bytes.Buffer
	if err := c.run(c.scriptCommand(args...), func() (io.Reader, error) { return strings.NewReader(script), nil }, &output); err != nil {
		return "", err
	}
	return strings.TrimSpace(output.String()), nil
}

func (c *Client) RunScript(script string, args ...string) error {
	return c.run(c.scriptCommand(args...), func() (io.Reader, error) { return strings.NewReader(script), nil }, c.out)
}

func (c *Client) RunShellInput(script string, input io.Reader, args ...string) error {
	parts := []string{"bash", "-c", script, "--"}
	parts = append(parts, args...)
	return c.RunWithInput(input, parts...)
}

func (c *Client) RunWithInput(input io.Reader, command ...string) error {
	open, cleanup, err := replayInput(input)
	if err != nil {
		return err
	}
	defer cleanup()
	return c.run(joinCommand(command), open, c.out)
}

func (c *Client) scriptCommand(args ...string) string {
	parts := []string{"bash", "-s", "--"}
	parts = append(parts, args...)
	return joinCommand(parts)
}

func (c *Client) command(remoteCommand string) *exec.Cmd {
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
	cmd := exec.Command("ssh", args...)
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

func describe(err error) error {
	if err == nil {
		return nil
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return &CommandError{ExitCode: exitErr.ExitCode()}
	}
	return fmt.Errorf("run ssh: %w", err)
}
