package vm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"incidentctl/internal/config"
)

var (
	ErrTokenRequired = errors.New("token is required")
	ErrTokenExpired  = errors.New("token expired")
)

type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("VM service returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("VM service returned HTTP %d: %s", e.StatusCode, e.Message)
}

type Capabilities struct {
	DefaultProfile string `json:"default_profile"`
}

type VirtualMachine struct {
	ID string `json:"id"`
}

type APIProvider interface {
	ConnectionProvider
	Capabilities(context.Context) (Capabilities, error)
	CreateVM(context.Context, *Request) (VirtualMachine, error)
	CleanupAllVMs(context.Context) error
	TerminalURL(context.Context, string) (string, error)
	SSHConnection(context.Context, string) (Connection, func(), error)
	SetToken(string)
}

type MicroVMServiceProvider struct {
	config config.MicroVMServiceConfig
	userID string

	mu           sync.RWMutex
	token        string
	capabilities Capabilities
	httpClient   *http.Client
}

func NewMicroVMServiceProvider(cfg config.MicroVMServiceConfig) *MicroVMServiceProvider {
	return NewMicroVMServiceProviderWithCredentials(cfg, "", "")
}

func NewMicroVMServiceProviderWithUserID(cfg config.MicroVMServiceConfig, userID string) *MicroVMServiceProvider {
	return NewMicroVMServiceProviderWithCredentials(cfg, userID, "")
}

func NewMicroVMServiceProviderWithCredentials(cfg config.MicroVMServiceConfig, userID, token string) *MicroVMServiceProvider {
	return &MicroVMServiceProvider{config: cfg, userID: userID, token: strings.TrimSpace(token)}
}

// UserID is retained for future API calls. User JWTs identify their owner in
// the token, so the service-user acting header is deliberately not sent.
func (p *MicroVMServiceProvider) UserID() string {
	return p.userID
}

func (p *MicroVMServiceProvider) SetToken(token string) {
	p.mu.Lock()
	p.token = strings.TrimSpace(token)
	p.mu.Unlock()
}

func (p *MicroVMServiceProvider) Capabilities(ctx context.Context) (Capabilities, error) {
	if p.currentToken() == "" {
		return Capabilities{}, ErrTokenRequired
	}
	var capabilities Capabilities
	if err := p.doJSON(ctx, http.MethodGet, "/v1/capabilities", nil, &capabilities, ""); err != nil {
		return Capabilities{}, err
	}
	p.mu.Lock()
	p.capabilities = capabilities
	p.mu.Unlock()
	return capabilities, nil
}

func (p *MicroVMServiceProvider) CreateVM(ctx context.Context, request *Request) (VirtualMachine, error) {
	if p.currentToken() == "" {
		return VirtualMachine{}, ErrTokenRequired
	}
	if request == nil {
		return VirtualMachine{}, errors.New("VM request is required")
	}
	if strings.TrimSpace(p.config.ImageID) == "" {
		return VirtualMachine{}, errors.New("vm_service.image_id is not configured")
	}

	profile := strings.TrimSpace(p.config.Profile)
	if profile == "" {
		p.mu.RLock()
		profile = p.capabilities.DefaultProfile
		p.mu.RUnlock()
	}
	body := createVMRequest{ImageID: p.config.ImageID, Profile: profile, TTL: p.config.TTLSeconds}
	var created operationAcceptedResponse
	if err := p.doJSON(ctx, http.MethodPost, "/v1/vms", body, &created, newIdempotencyKey()); err != nil {
		return VirtualMachine{}, err
	}
	operation, err := p.waitOperation(ctx, created.OperationID)
	if err != nil {
		return VirtualMachine{}, err
	}
	vmID := created.VMID
	if vmID == "" {
		vmID = operation.VMID
	}
	if vmID == "" {
		return VirtualMachine{}, errors.New("VM service did not return vm_id")
	}
	return VirtualMachine{ID: vmID}, nil
}

func (p *MicroVMServiceProvider) CleanupAllVMs(ctx context.Context) error {
	if p.currentToken() == "" {
		return ErrTokenRequired
	}
	var listed listVMResponse
	if err := p.doJSON(ctx, http.MethodGet, "/v1/vms", nil, &listed, ""); err != nil {
		return fmt.Errorf("list VMs: %w", err)
	}

	var cleanupErrors []error
	for _, instance := range listed.VMs {
		if strings.TrimSpace(instance.ID) == "" {
			cleanupErrors = append(cleanupErrors, errors.New("list VMs returned an empty VM id"))
			continue
		}
		if err := p.deleteVM(ctx, instance.ID); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("delete VM %s: %w", instance.ID, err))
		}
	}
	return errors.Join(cleanupErrors...)
}

func (p *MicroVMServiceProvider) deleteVM(ctx context.Context, vmID string) error {
	if strings.TrimSpace(vmID) == "" {
		return errors.New("VM id is required")
	}
	var accepted operationAcceptedResponse
	path := "/v1/vms/" + url.PathEscape(vmID)
	if err := p.doJSON(ctx, http.MethodDelete, path, nil, &accepted, newIdempotencyKey()); err != nil {
		return err
	}
	if _, err := p.waitOperation(ctx, accepted.OperationID); err != nil {
		return err
	}
	return nil
}

func (p *MicroVMServiceProvider) TerminalURL(ctx context.Context, vmID string) (string, error) {
	if p.currentToken() == "" {
		return "", ErrTokenRequired
	}
	if strings.TrimSpace(vmID) == "" {
		return "", errors.New("VM id is required")
	}
	var started terminalResponse
	path := "/v1/vms/" + url.PathEscape(vmID) + "/terminal"
	if err := p.doJSON(ctx, http.MethodPost, path, map[string]any{}, &started, newIdempotencyKey()); err != nil {
		return "", err
	}
	operation, err := p.waitOperation(ctx, started.OperationID)
	if err != nil {
		return "", err
	}
	if operation.Result.URL == "" {
		return "", errors.New("VM service did not return terminal URL")
	}
	return operation.Result.URL, nil
}

func (p *MicroVMServiceProvider) SSHConnection(ctx context.Context, vmID string) (Connection, func(), error) {
	if p.currentToken() == "" {
		return Connection{}, nil, ErrTokenRequired
	}
	if strings.TrimSpace(vmID) == "" {
		return Connection{}, nil, errors.New("VM id is required")
	}
	waitCtx, cancel := context.WithTimeout(ctx, time.Duration(p.config.TimeoutSeconds)*time.Second)
	defer cancel()
	for {
		var metadata sshMetadata
		path := "/v1/vms/" + url.PathEscape(vmID) + "/ssh"
		if err := p.doJSON(waitCtx, http.MethodGet, path, nil, &metadata, ""); err != nil {
			return Connection{}, nil, err
		}
		if metadata.Ready {
			if metadata.Host == "" || metadata.Port < 1 || metadata.User == "" {
				return Connection{}, nil, errors.New("VM service returned incomplete SSH metadata")
			}
			return p.connectionFromSSHMetadata(waitCtx, vmID, metadata)
		}
		select {
		case <-waitCtx.Done():
			return Connection{}, nil, fmt.Errorf("wait for SSH readiness: %w", waitCtx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (p *MicroVMServiceProvider) connectionFromSSHMetadata(ctx context.Context, vmID string, metadata sshMetadata) (Connection, func(), error) {
	var key sshKeyResponse
	if err := p.doJSON(ctx, http.MethodGet, "/v1/vms/"+url.PathEscape(vmID)+"/ssh-key", nil, &key, ""); err != nil {
		return Connection{}, nil, err
	}
	if key.PrivateKey == "" {
		return Connection{}, nil, errors.New("VM service did not return an SSH private key")
	}
	file, err := os.CreateTemp("", "incidentctl-ssh-*")
	if err != nil {
		return Connection{}, nil, fmt.Errorf("create temporary SSH key: %w", err)
	}
	path := file.Name()
	removeKey := func() { _ = os.Remove(path) }
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		removeKey()
		return Connection{}, nil, fmt.Errorf("protect temporary SSH key: %w", err)
	}
	if _, err := file.WriteString(key.PrivateKey); err != nil {
		_ = file.Close()
		removeKey()
		return Connection{}, nil, fmt.Errorf("write temporary SSH key: %w", err)
	}
	if err := file.Close(); err != nil {
		removeKey()
		return Connection{}, nil, fmt.Errorf("close temporary SSH key: %w", err)
	}
	var bastion *Bastion
	if metadata.Bastion.Host != "" && metadata.Bastion.Port > 0 && metadata.Bastion.User != "" {
		bastion = &Bastion{Host: metadata.Bastion.Host, Port: metadata.Bastion.Port, User: metadata.Bastion.User}
	}
	return Connection{
		Host:                  metadata.Host,
		Port:                  metadata.Port,
		User:                  metadata.User,
		IdentityFile:          path,
		PrivateKey:            key.PrivateKey,
		Bastion:               bastion,
		StrictHostKeyChecking: false,
	}, removeKey, nil
}

// Connection remains available for the legacy SSH implementation. The TUI
// uses the API terminal flow, but callers that still use SSH are unchanged.
func (p *MicroVMServiceProvider) Connection(context.Context, *Request) (Connection, error) {
	_, _, _ = p.config, p.userID, p.currentToken()
	return Connection{}, ErrMicroVMServiceNotImplemented
}

type createVMRequest struct {
	ImageID string `json:"image_id"`
	Profile string `json:"profile,omitempty"`
	TTL     int    `json:"ttl_seconds,omitempty"`
}

type operationAcceptedResponse struct {
	OperationID string `json:"operation_id"`
	VMID        string `json:"vm_id"`
}

type listVMResponse struct {
	VMs []VirtualMachine `json:"vms"`
}

type terminalResponse struct {
	OperationID string `json:"operation_id"`
}

type sshMetadata struct {
	VMID               string `json:"vm_id"`
	Host               string `json:"host"`
	Port               int    `json:"port"`
	User               string `json:"user"`
	HostKeyFingerprint string `json:"host_key_fingerprint"`
	Ready              bool   `json:"ready"`
	AuthMode           string `json:"auth_mode"`
	Bastion            struct {
		Host string `json:"host"`
		Port int    `json:"port"`
		User string `json:"user"`
	} `json:"bastion"`
}

type sshKeyResponse struct {
	PrivateKey string `json:"private_key"`
}

type operationResponse struct {
	Status string `json:"status"`
	VMID   string `json:"vm_id"`
	Result struct {
		URL string `json:"url"`
	} `json:"result"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (p *MicroVMServiceProvider) waitOperation(ctx context.Context, operationID string) (operationResponse, error) {
	if strings.TrimSpace(operationID) == "" {
		return operationResponse{}, errors.New("VM service did not return operation_id")
	}
	waitCtx, cancel := context.WithTimeout(ctx, time.Duration(p.config.TimeoutSeconds)*time.Second)
	defer cancel()
	for {
		var operation operationResponse
		path := "/v1/operations/" + url.PathEscape(operationID)
		if err := p.doJSON(waitCtx, http.MethodGet, path, nil, &operation, ""); err != nil {
			return operationResponse{}, err
		}
		switch strings.ToLower(operation.Status) {
		case "succeeded":
			return operation, nil
		case "failed":
			message := operation.Error.Message
			if message == "" {
				message = "VM operation failed"
			}
			return operationResponse{}, errors.New(message)
		}
		select {
		case <-waitCtx.Done():
			return operationResponse{}, fmt.Errorf("wait for VM operation: %w", waitCtx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (p *MicroVMServiceProvider) doJSON(ctx context.Context, method, path string, body any, result any, idempotencyKey string) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode VM service request: %w", err)
		}
		reader = strings.NewReader(string(encoded))
	}
	endpoint, err := p.endpoint(path)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("create VM service request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if token := p.currentToken(); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}

	client := p.httpClient
	if client == nil {
		client = &http.Client{Timeout: time.Duration(p.config.TimeoutSeconds) * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("VM service request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message, code := decodeAPIError(response.Body)
		if response.StatusCode == http.StatusUnauthorized {
			if message == "" {
				message = "token expired or invalid"
			}
			return fmt.Errorf("%w: %s", ErrTokenExpired, message)
		}
		return &APIError{StatusCode: response.StatusCode, Code: code, Message: message}
	}
	if result == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(result); err != nil {
		return fmt.Errorf("decode VM service response: %w", err)
	}
	return nil
}

func (p *MicroVMServiceProvider) endpoint(path string) (string, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(p.config.BaseURL), "/"))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", errors.New("invalid VM service base URL")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/" + strings.TrimLeft(path, "/")
	base.RawQuery = ""
	base.Fragment = ""
	return base.String(), nil
}

func (p *MicroVMServiceProvider) currentToken() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.token
}

func newIdempotencyKey() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return fmt.Sprintf("incidentctl-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(bytes)
}

func decodeAPIError(reader io.Reader) (message, code string) {
	var payload struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Detail  string `json:"detail"`
		Error   struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(reader).Decode(&payload); err != nil {
		return "", ""
	}
	message = payload.Message
	if message == "" {
		message = payload.Detail
	}
	if message == "" {
		message = payload.Error.Message
	}
	if code == "" {
		code = payload.Code
	}
	if code == "" {
		code = payload.Error.Code
	}
	return message, code
}
