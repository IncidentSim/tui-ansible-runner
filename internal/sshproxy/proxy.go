// Package sshproxy provides the stdio transport used by OpenSSH ProxyCommand.
package sshproxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const Environment = "INCIDENTCTL_SSH_PROXY"

// Parse accepts unauthenticated TCP proxies. Restrict the authority to characters
// safe to include in the ProxyCommand interpreted by the local SSH shell.
func Parse(value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" {
		return nil, errors.New("SSH proxy must be an absolute http, https or socks5 URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" && u.Scheme != "socks5h" {
		return nil, errors.New("SSH proxy scheme must be http, https or socks5")
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("SSH proxy URL must contain only scheme, host and port; authentication is not supported")
	}
	for _, c := range u.Host {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789.-_:[]", c) {
			return nil, errors.New("SSH proxy host contains unsupported characters")
		}
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, errors.New("SSH proxy port must be between 1 and 65535")
		}
	}
	return u, nil
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func (c *bufferedConn) CloseWrite() error {
	if half, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return half.CloseWrite()
	}
	return nil
}

// Dial negotiates a tunnel and preserves bytes buffered after the proxy reply.
func Dial(ctx context.Context, value, host string, port int, timeout time.Duration) (net.Conn, error) {
	u, err := Parse(value)
	if err != nil {
		return nil, err
	}
	if host == "" || strings.ContainsAny(host, " \t\r\n\x00") || port < 1 || port > 65535 {
		return nil, errors.New("invalid SSH tunnel destination")
	}
	proxyPort := u.Port()
	if proxyPort == "" {
		proxyPort = map[string]string{"http": "80", "https": "443", "socks5": "1080", "socks5h": "1080"}[u.Scheme]
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), proxyPort))
	if err != nil {
		return nil, fmt.Errorf("connect to SSH proxy: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	rawConn := conn
	stopCancel := context.AfterFunc(ctx, func() { rawConn.Close() })
	defer stopCancel()
	if u.Scheme == "https" {
		secured := tls.Client(conn, &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12})
		if err := secured.HandshakeContext(ctx); err != nil {
			return nil, fmt.Errorf("SSH proxy TLS handshake: %w", err)
		}
		conn = secured
	}
	reader := bufio.NewReader(conn)
	if strings.HasPrefix(u.Scheme, "socks5") {
		err = socksConnect(conn, reader, host, port)
	} else {
		target := net.JoinHostPort(host, strconv.Itoa(port))
		if _, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); err == nil {
			var response *http.Response
			response, err = http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
			if err == nil && response.StatusCode != http.StatusOK {
				err = fmt.Errorf("HTTP CONNECT rejected (status %d)", response.StatusCode)
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("SSH proxy tunnel: %w", err)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	ok = true
	return &bufferedConn{Conn: conn, reader: reader}, nil
}

func socksConnect(conn net.Conn, reader io.Reader, host string, port int) error {
	if len(host) > 255 {
		return errors.New("SOCKS5 destination name is too long")
	}
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		return err
	}
	var reply [2]byte
	if _, err := io.ReadFull(reader, reply[:]); err != nil {
		return err
	}
	if reply != [2]byte{5, 0} {
		return errors.New("SOCKS5 proxy does not accept unauthenticated connections")
	}
	request := append([]byte{5, 1, 0, 3, byte(len(host))}, []byte(host)...)
	request = append(request, byte(port>>8), byte(port))
	if _, err := conn.Write(request); err != nil {
		return err
	}
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return err
	}
	if header[0] != 5 || header[1] != 0 || header[2] != 0 {
		return fmt.Errorf("SOCKS5 CONNECT rejected (code %d)", header[1])
	}
	length := 0
	switch header[3] {
	case 1:
		length = 4
	case 4:
		length = 16
	case 3:
		var size [1]byte
		if _, err := io.ReadFull(reader, size[:]); err != nil {
			return err
		}
		length = int(size[0])
	default:
		return errors.New("invalid SOCKS5 reply address")
	}
	_, err := io.CopyN(io.Discard, reader, int64(length+2))
	return err
}

// Relay connects OpenSSH's stdin/stdout to the proxy tunnel. Only diagnostics
// belong on stderr; stdout must remain an unmodified SSH byte stream.
func Relay(ctx context.Context, value, host string, port int, input io.Reader, output io.Writer) error {
	conn, err := Dial(ctx, value, host, port, 10*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	go func() {
		if _, err := io.Copy(conn, input); err != nil {
			conn.Close()
			return
		}
		conn.(interface{ CloseWrite() error }).CloseWrite()
	}()
	_, err = io.Copy(output, conn)
	return err
}
