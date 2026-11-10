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

	"incidentctl/internal/config"
)

type Client struct {
	config config.TargetConfig
	out    io.Writer
	errOut io.Writer
}

func New(cfg config.TargetConfig) *Client {
	return &Client{config: cfg, out: os.Stdout, errOut: os.Stderr}
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
	}
	if c.config.IdentityFile != "" {
		args = append(args, "-i", c.config.IdentityFile)
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
		return fmt.Errorf("remote command exited with code %d", exitErr.ExitCode())
	}
	return fmt.Errorf("run ssh: %w", err)
}
