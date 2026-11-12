package remote

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const sshAttempts = 3
const sshLogBytes = 128 << 10

// replayInput keeps retryable uploads on disk rather than buffering archives in
// RAM. Seekable inputs retain their caller-supplied starting offset.
func replayInput(input io.Reader) (func() (io.Reader, error), func(), error) {
	if input == nil {
		return func() (io.Reader, error) { return nil, nil }, func() {}, nil
	}
	if seeker, ok := input.(io.ReadSeeker); ok {
		offset, err := seeker.Seek(0, io.SeekCurrent)
		if err == nil {
			return func() (io.Reader, error) {
				_, err := seeker.Seek(offset, io.SeekStart)
				return seeker, err
			}, func() {}, nil
		}
	}
	file, err := os.CreateTemp("", "incidentctl-ssh-input-*")
	if err != nil {
		return nil, nil, fmt.Errorf("stage SSH input: %w", err)
	}
	cleanup := func() { file.Close(); os.Remove(file.Name()) }
	if _, err := io.Copy(file, input); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("stage SSH input: %w", err)
	}
	return func() (io.Reader, error) {
		_, err := file.Seek(0, io.SeekStart)
		return file, err
	}, cleanup, nil
}

type outputCounter struct {
	writer io.Writer
	bytes  int64
}

func (w *outputCounter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	w.bytes += int64(n)
	return n, err
}

type diagnosticTail []byte

func (tail *diagnosticTail) Write(p []byte) (int, error) {
	const limit = 4096
	n := len(p)
	if len(p) >= limit {
		*tail = append((*tail)[:0], p[len(p)-limit:]...)
	} else {
		if len(*tail)+len(p) > limit {
			*tail = (*tail)[len(*tail)+len(p)-limit:]
		}
		*tail = append(*tail, p...)
	}
	return n, nil
}

func readSSHLog(name string) (string, bool) {
	file, err := os.Open(name)
	if err != nil {
		return "", false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, sshLogBytes+1))
	return string(data), err == nil && len(data) <= sshLogBytes
}

// Retry decisions use only OpenSSH's own -E log, never remote stderr (which a
// playbook can print). Any evidence of authentication, a command or a reused
// master connection blocks replay, even when the final status is 255.
func retryBeforeCommand(log, helperErrors string, stdoutBytes int64) bool {
	if stdoutBytes != 0 || strings.Contains(log, "Authenticated to ") ||
		strings.Contains(log, "Authentication succeeded") || strings.Contains(log, "Sending command:") ||
		strings.Contains(log, "mux_client") {
		return false
	}
	for _, permanent := range []string{"HTTP CONNECT rejected", "does not accept unauthenticated", "certificate", "invalid SSH tunnel", "SSH proxy URL"} {
		if strings.Contains(helperErrors, permanent) {
			return false
		}
	}
	for _, line := range strings.Split(log, "\n") {
		line = strings.TrimSpace(line)
		if line == "Connection timed out during banner exchange" ||
			strings.HasPrefix(line, "banner exchange: ") ||
			strings.HasPrefix(line, "kex_exchange_identification: Connection closed by remote host") ||
			strings.HasPrefix(line, "kex_exchange_identification: read: Connection reset by peer") ||
			(strings.HasPrefix(line, "ssh: connect to host ") &&
				(strings.HasSuffix(line, "Connection timed out") || strings.HasSuffix(line, "Connection refused"))) {
			return true
		}
	}
	return false
}

func printSSHFailure(writer io.Writer, log string) {
	for _, line := range strings.Split(log, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "debug") || strings.HasPrefix(line, "OpenSSH_") || strings.HasPrefix(line, "Authenticated to ") ||
			strings.HasPrefix(line, "Transferred:") || strings.HasPrefix(line, "Bytes per second:") {
			continue
		}
		fmt.Fprintln(writer, line)
	}
}

func (c *Client) run(command string, input func() (io.Reader, error), output io.Writer) error {
	for attempt := 1; attempt <= sshAttempts; attempt++ {
		file, err := os.CreateTemp("", "incidentctl-ssh-*.log")
		if err != nil {
			return fmt.Errorf("create SSH diagnostics: %w", err)
		}
		name := file.Name()
		if err := file.Close(); err != nil {
			os.Remove(name)
			return err
		}
		reader, err := input()
		if err != nil {
			os.Remove(name)
			return err
		}
		cmd := c.command(command)
		cmd.Args = append([]string{cmd.Args[0], "-v", "-E", name, "-o", "LogLevel=DEBUG1"}, cmd.Args[1:]...)
		if c.attemptCommand != nil {
			cmd = c.attemptCommand(command, name)
		}
		counter := &outputCounter{writer: output}
		var helperErrors diagnosticTail
		cmd.Stdin, cmd.Stdout, cmd.Stderr = reader, counter, io.MultiWriter(c.errOut, &helperErrors)
		err = describe(cmd.Run())
		log, complete := readSSHLog(name)
		os.Remove(name)
		if err == nil {
			return nil
		}
		printSSHFailure(c.errOut, log)
		var exit *CommandError
		safe := errors.As(err, &exit) && exit.ExitCode == 255 && complete && retryBeforeCommand(log, string(helperErrors), counter.bytes)
		if safe {
			exit.BeforeCommand, exit.Attempts = true, attempt
		}
		if !safe || attempt == sshAttempts {
			if exit != nil && exit.ExitCode == 255 {
				return fmt.Errorf("SSH %s:%d (attempt %d/%d): %w", c.config.Host, c.config.Port, attempt, sshAttempts, err)
			}
			return err
		}
		fmt.Fprintf(c.errOut, "SSH %s:%d: соединение прервалось до запуска команды; повтор %d/%d…\n", c.config.Host, c.config.Port, attempt+1, sshAttempts)
		pause := c.retrySleep
		if pause == nil {
			pause = time.Sleep
		}
		pause(time.Duration(attempt) * time.Second)
	}
	panic("unreachable SSH retry state")
}
