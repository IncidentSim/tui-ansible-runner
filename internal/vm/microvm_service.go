package vm

import (
	"context"

	"incidentctl/internal/config"
)

type MicroVMServiceProvider struct {
	config config.MicroVMServiceConfig
	userID string
}

func NewMicroVMServiceProvider(cfg config.MicroVMServiceConfig) *MicroVMServiceProvider {
	return NewMicroVMServiceProviderWithUserID(cfg, "")
}

func NewMicroVMServiceProviderWithUserID(cfg config.MicroVMServiceConfig, userID string) *MicroVMServiceProvider {
	return &MicroVMServiceProvider{config: cfg, userID: userID}
}

// UserID is the stable device-bound identifier that the future API request
// will send as the user ID.
func (p *MicroVMServiceProvider) UserID() string {
	return p.userID
}

func (p *MicroVMServiceProvider) Connection(context.Context, *Request) (Connection, error) {
	_, _ = p.config, p.userID
	return Connection{}, ErrMicroVMServiceNotImplemented
}
