package vm

import (
	"context"

	"incidentctl/internal/config"
)

type MicroVMServiceProvider struct {
	config config.MicroVMServiceConfig
}

func NewMicroVMServiceProvider(cfg config.MicroVMServiceConfig) *MicroVMServiceProvider {
	return &MicroVMServiceProvider{config: cfg}
}

func (p *MicroVMServiceProvider) Connection(context.Context, *Request) (Connection, error) {
	_ = p.config
	return Connection{}, ErrMicroVMServiceNotImplemented
}
