package main

import (
	"fmt"

	"incidentctl/internal/config"
	"incidentctl/internal/exercises"
	"incidentctl/internal/vm"
)

func buildConnectionProvider(providerName, directConfigPath, microVMConfigPath string) (vm.ConnectionProvider, string, error) {
	switch providerName {
	case "direct":
		cfg, err := config.Load(directConfigPath)
		if err != nil {
			return nil, "", err
		}
		return vm.NewDirectProvider(cfg.Target), cfg.Paths.RemoteWorkDir, nil
	case "microvm-service":
		cfg, err := config.LoadMicroVMService(microVMConfigPath)
		if err != nil {
			return nil, "", err
		}
		return vm.NewMicroVMServiceProvider(cfg), cfg.RemoteWorkDir, nil
	default:
		return nil, "", fmt.Errorf("unknown VM provider %q (want direct or microvm-service)", providerName)
	}
}

func buildExerciseSource(sourceName, localRoot, repositoryConfigPath string) (exercises.Source, error) {
	switch sourceName {
	case "local":
		return exercises.NewLocalSource(localRoot), nil
	case "git":
		cfg, err := config.LoadExerciseRepository(repositoryConfigPath)
		if err != nil {
			return nil, err
		}
		return exercises.NewGitSource(cfg), nil
	default:
		return nil, fmt.Errorf("unknown exercise source %q (want local or git)", sourceName)
	}
}
