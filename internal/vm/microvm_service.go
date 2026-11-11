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
	ID string
}

type APIProvider interface {
	ConnectionProvider
	Capabilities(context.Context) (Capabilities, error)
	CreateVM(context.Context, *Request) (VirtualMachine, error)
	TerminalURL(context.Context, string) (string, error)
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
	var created createVMResponse
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

type createVMResponse struct {
	OperationID string `json:"operation_id"`
	VMID        string `json:"vm_id"`
}

type terminalResponse struct {
	OperationID string `json:"operation_id"`
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
