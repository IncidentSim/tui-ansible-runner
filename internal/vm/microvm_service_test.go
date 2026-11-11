package vm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"incidentctl/internal/config"
)

func TestMicroVMServiceProviderUsesCapabilitiesVMAndTerminalAPI(t *testing.T) {
	var operationCalls int
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer valid-token" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		if request.Header.Get("X-Acting-User-ID") != "" {
			t.Error("user bearer must not send X-Acting-User-ID")
		}
		switch request.Method + " " + request.URL.Path {
		case http.MethodGet + " /v1/capabilities":
			return jsonResponse(http.StatusOK, `{"default_profile":"small"}`), nil
		case http.MethodPost + " /v1/vms":
			if request.Header.Get("Idempotency-Key") == "" {
				t.Error("create VM request has no idempotency key")
			}
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode create VM body: %v", err)
			}
			if body["image_id"] != "ubuntu-base" || body["profile"] != "small" {
				t.Errorf("unexpected create VM body: %#v", body)
			}
			return jsonResponse(http.StatusAccepted, `{"operation_id":"create-op"}`), nil
		case http.MethodGet + " /v1/operations/create-op":
			operationCalls++
			return jsonResponse(http.StatusOK, `{"status":"succeeded","vm_id":"vm-123"}`), nil
		case http.MethodPost + " /v1/vms/vm-123/terminal":
			if request.Header.Get("Idempotency-Key") == "" {
				t.Error("terminal request has no idempotency key")
			}
			return jsonResponse(http.StatusAccepted, `{"operation_id":"terminal-op"}`), nil
		case http.MethodGet + " /v1/operations/terminal-op":
			return jsonResponse(http.StatusOK, `{"status":"succeeded","result":{"url":"https://terminal.example/session"}}`), nil
		case http.MethodGet + " /v1/vms/vm-123/ssh":
			return jsonResponse(http.StatusOK, `{"host":"vm.example","port":2222,"user":"root","ready":true}`), nil
		case http.MethodGet + " /v1/vms/vm-123/ssh-key":
			return jsonResponse(http.StatusOK, `{"vm_id":"vm-123","private_key":"PRIVATE KEY"}`), nil
		default:
			return jsonResponse(http.StatusNotFound, `{}`), nil
		}
	})
	provider := NewMicroVMServiceProviderWithCredentials(config.MicroVMServiceConfig{
		BaseURL:        "https://service.example",
		TimeoutSeconds: 2,
		ImageID:        "ubuntu-base",
		RemoteWorkDir:  "/tmp/incidentctl",
	}, "device-id", "valid-token")
	provider.httpClient = &http.Client{Transport: transport}

	if _, err := provider.Capabilities(context.Background()); err != nil {
		t.Fatal(err)
	}
	instance, err := provider.CreateVM(context.Background(), &Request{ExerciseName: "exercise"})
	if err != nil {
		t.Fatal(err)
	}
	if instance.ID != "vm-123" {
		t.Fatalf("VM id = %q", instance.ID)
	}
	terminalURL, err := provider.TerminalURL(context.Background(), instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	if terminalURL != "https://terminal.example/session" {
		t.Fatalf("terminal URL = %q", terminalURL)
	}
	if operationCalls != 1 {
		t.Fatalf("operation calls = %d, want 1", operationCalls)
	}
	connection, cleanup, err := provider.SSHConnection(context.Background(), instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	if connection.Host != "vm.example" || connection.Port != 2222 || connection.User != "root" {
		t.Fatalf("unexpected SSH connection: %+v", connection)
	}
	key, err := os.ReadFile(connection.IdentityFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(key) != "PRIVATE KEY" {
		t.Fatalf("SSH key = %q", key)
	}
	keyPath := connection.IdentityFile
	cleanup()
	if _, err := os.Stat(keyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary SSH key still exists: %v", err)
	}
}

func TestMicroVMServiceProviderReportsUnauthorizedToken(t *testing.T) {
	provider := NewMicroVMServiceProviderWithCredentials(config.MicroVMServiceConfig{
		BaseURL:        "https://service.example",
		TimeoutSeconds: 2,
	}, "device-id", "expired-token")
	provider.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusUnauthorized, `{"error":{"code":"unauthorized","message":"expired"}}`), nil
	})}
	_, err := provider.Capabilities(context.Background())
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("error = %v, want ErrTokenExpired", err)
	}
}

func TestMicroVMServiceProviderCleansUpAllVMs(t *testing.T) {
	var deleted []string
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/vms":
			return jsonResponse(http.StatusOK, `{"vms":[{"id":"vm-1"},{"id":"vm-2"}]}`), nil
		case request.Method == http.MethodDelete && strings.HasPrefix(request.URL.Path, "/v1/vms/"):
			if request.Header.Get("Idempotency-Key") == "" {
				t.Error("delete VM request has no idempotency key")
			}
			id := strings.TrimPrefix(request.URL.Path, "/v1/vms/")
			deleted = append(deleted, id)
			return jsonResponse(http.StatusAccepted, `{"operation_id":"delete-`+id+`"}`), nil
		case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1/operations/delete-"):
			return jsonResponse(http.StatusOK, `{"status":"succeeded"}`), nil
		default:
			return jsonResponse(http.StatusNotFound, `{}`), nil
		}
	})
	provider := NewMicroVMServiceProviderWithCredentials(config.MicroVMServiceConfig{
		BaseURL:        "https://service.example",
		TimeoutSeconds: 2,
	}, "device-id", "valid-token")
	provider.httpClient = &http.Client{Transport: transport}

	if err := provider.CleanupAllVMs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(deleted, ",") != "vm-1,vm-2" {
		t.Fatalf("deleted VMs = %v, want [vm-1 vm-2]", deleted)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
