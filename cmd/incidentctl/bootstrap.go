package main

import (
	"errors"
	"fmt"

	"incidentctl/internal/config"
	"incidentctl/internal/exercises"
	"incidentctl/internal/identity"
	"incidentctl/internal/vm"
)

func buildRuntime(configPath string) (exercises.Source, vm.ConnectionProvider, string, string, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, nil, "", "", err
	}
	userID, err := identity.LoadOrCreate(cfg.UserIDFile)
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("load user ID: %w", err)
	}
	tokenPath, err := identity.ResolveTokenPath(cfg.TokenFile)
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("resolve token path: %w", err)
	}
	tokenRecord, err := identity.LoadToken(tokenPath)
	token := ""
	if err == nil {
		token = tokenRecord.Token
	} else if !errors.Is(err, identity.ErrTokenNotFound) {
		// A broken or unreadable token must not prevent browsing the remote
		// exercise catalog. The TUI can replace it through token settings.
		token = ""
	}
	return exercises.NewGitSource(cfg.ExerciseRepository),
		vm.NewMicroVMServiceProviderWithCredentials(cfg.VMService, userID, token),
		cfg.VMService.RemoteWorkDir, tokenPath, nil
}
