package sshproxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestParseProxy(t *testing.T) {
	for _, value := range []string{"http://127.0.0.1:10809", "https://proxy.example", "socks5://[::1]:1080", "socks5h://localhost"} {
		if _, err := Parse(value); err != nil {
			t.Fatalf("%s: %v", value, err)
		}
	}
	for _, value := range []string{"", "127.0.0.1:10809", "ftp://proxy", "http://user:secret@proxy", "http://proxy:0", "http://proxy:65536", "http://proxy/command", "http://proxy?q=1", "http://proxy#fragment", "http://$(whoami):80", "http://proxy\n"} {
		if _, err := Parse(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}

func serve(t *testing.T, handler func(net.Conn) error) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(3 * time.Second))
			err = handler(conn)
		}
		done <- err
	}()
	t.Cleanup(func() {
		listener.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(4 * time.Second):
			t.Error("proxy server did not stop")
		}
	})
	return listener.Addr().String()
}

func TestHTTPConnectRelaysBothDirectionsAndBufferedBanner(t *testing.T) {
	address := serve(t, func(conn net.Conn) error {
		reader := bufio.NewReader(conn)
		req, err := http.ReadRequest(reader)
		if err != nil {
			return err
		}
		if req.Method != "CONNECT" || req.Host != "vm.example:22000" {
			return fmt.Errorf("unexpected CONNECT: %s %s", req.Method, req.Host)
		}
		if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\nSSH-2.0-test\r\n"); err != nil {
			return err
		}
		line, err := reader.ReadString('\n')
		if err != nil || line != "client-data\n" {
			return fmt.Errorf("unexpected client data %q: %v", line, err)
		}
		_, err = io.WriteString(conn, "server-data\n")
		return err
	})
	conn, err := Dial(context.Background(), "http://"+address, "vm.example", 22000, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	if line, err := reader.ReadString('\n'); err != nil || line != "SSH-2.0-test\r\n" {
		t.Fatalf("lost buffered banner %q: %v", line, err)
	}
	if _, err := io.WriteString(conn, "client-data\n"); err != nil {
		t.Fatal(err)
	}
	if line, err := reader.ReadString('\n'); err != nil || line != "server-data\n" {
		t.Fatalf("tunnel response %q: %v", line, err)
	}
}

func TestSOCKS5ResolvesDestinationAtProxy(t *testing.T) {
	address := serve(t, func(conn net.Conn) error {
		greeting := make([]byte, 3)
		if _, err := io.ReadFull(conn, greeting); err != nil {
			return err
		}
		if string(greeting) != "\x05\x01\x00" {
			return fmt.Errorf("unexpected SOCKS greeting %v", greeting)
		}
		conn.Write([]byte{5, 0})
		header := make([]byte, 5)
		if _, err := io.ReadFull(conn, header); err != nil {
			return err
		}
		name := make([]byte, int(header[4])+2)
		if _, err := io.ReadFull(conn, name); err != nil {
			return err
		}
		if string(header[:4]) != "\x05\x01\x00\x03" || string(name[:len(name)-2]) != "vm.invalid" || int(name[len(name)-2])<<8|int(name[len(name)-1]) != 22000 {
			return fmt.Errorf("unexpected destination: %v %v", header, name)
		}
		_, err := conn.Write(append([]byte{5, 0, 0, 4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0}, []byte("SSH-banner")...))
		return err
	})
	conn, err := Dial(context.Background(), "socks5://"+address, "vm.invalid", 22000, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	data, err := io.ReadAll(conn)
	if err != nil || string(data) != "SSH-banner" {
		t.Fatalf("banner %q: %v", data, err)
	}
}

func TestProxyRejectsConnectWithoutDirectFallback(t *testing.T) {
	address := serve(t, func(conn net.Conn) error {
		if _, err := http.ReadRequest(bufio.NewReader(conn)); err != nil {
			return err
		}
		_, err := io.WriteString(conn, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
		return err
	})
	if _, err := Dial(context.Background(), "http://"+address, "vm.invalid", 22000, time.Second); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("expected proxy rejection, got %v", err)
	}
}

func TestProxyHandshakeTimeout(t *testing.T) {
	address := serve(t, func(conn net.Conn) error {
		_, err := io.Copy(io.Discard, conn)
		return err
	})
	started := time.Now()
	if _, err := Dial(context.Background(), "http://"+address, "vm.invalid", 22, 50*time.Millisecond); err == nil {
		t.Fatal("expected timeout")
	}
	if time.Since(started) > time.Second {
		t.Fatal("proxy handshake exceeded timeout")
	}
}
