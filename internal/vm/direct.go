package vm

import (
	"context"

	"incidentctl/internal/config"
)

type DirectProvider struct {
	target config.TargetConfig
}

func NewDirectProvider(target config.TargetConfig) *DirectProvider {
	return &DirectProvider{target: target}
}

func (p *DirectProvider) Connection(context.Context, *Request) (Connection, error) {
	return Connection{
		Host:                  p.target.Host,
		Port:                  p.target.Port,
		User:                  p.target.User,
		IdentityFile:          p.target.IdentityFile,
		StrictHostKeyChecking: p.target.StrictHostKeyChecking,
	}, nil
}
