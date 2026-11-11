package main

import (
	"fmt"

	"incidentctl/internal/config"
	"incidentctl/internal/exercises"
	"incidentctl/internal/identity"
	"incidentctl/internal/vm"
)

func buildRuntime(configPath string) (exercises.Source, vm.ConnectionProvider, string, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, nil, "", err
	}
	userID, err := identity.LoadOrCreate(cfg.UserIDFile)
	if err != nil {
		return nil, nil, "", fmt.Errorf("load user ID: %w", err)
	}
	return exercises.NewGitSource(cfg.ExerciseRepository),
		vm.NewMicroVMServiceProviderWithUserID(cfg.VMService, userID), cfg.VMService.RemoteWorkDir, nil
}
