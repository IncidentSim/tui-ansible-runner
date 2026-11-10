package vm

import (
	"context"
	"errors"

	"incidentctl/internal/config"
)

var ErrMicroVMServiceNotImplemented = errors.New("microVM service provider is not implemented")

type Connection struct {
	Host                  string
	IP                    string
	Port                  int
	User                  string
	Password              *string
	IdentityFile          string
	StrictHostKeyChecking bool
}

type Request struct {
	ExerciseName string
	VCPUCount    int
	MemoryMiB    int
	OSID         string
	OSVersion    string
}

func RequestFromExercise(exercise config.Exercise) *Request {
	return &Request{
		ExerciseName: exercise.Name,
		VCPUCount:    exercise.Resources.VCPUCount,
		MemoryMiB:    exercise.Resources.MemoryMiB,
		OSID:         exercise.Runtime.OS.ID,
		OSVersion:    exercise.Runtime.OS.Version,
	}
}

type ConnectionProvider interface {
	Connection(context.Context, *Request) (Connection, error)
}
