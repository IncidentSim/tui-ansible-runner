package vm

import (
	"context"
	"errors"
	"testing"

	"incidentctl/internal/config"
)

func TestMicroVMServiceProviderIsExplicitStub(t *testing.T) {
	t.Parallel()
	provider := NewMicroVMServiceProvider(config.MicroVMServiceConfig{BaseURL: "https://microvm.example"})
	_, err := provider.Connection(context.Background(), &Request{ExerciseName: "exercise"})
	if !errors.Is(err, ErrMicroVMServiceNotImplemented) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRequestFromExerciseCopiesVMRequirements(t *testing.T) {
	t.Parallel()
	request := RequestFromExercise(config.Exercise{
		Name:      "nginx",
		Resources: config.Resources{VCPUCount: 2, MemoryMiB: 1024},
		Runtime:   config.Runtime{OS: config.OperatingSystem{ID: "ubuntu", Version: "24.04"}},
	})
	if request.ExerciseName != "nginx" || request.VCPUCount != 2 || request.MemoryMiB != 1024 || request.OSID != "ubuntu" || request.OSVersion != "24.04" {
		t.Fatalf("unexpected request: %+v", request)
	}
}
