package main

import (
	"fmt"
	"runtime"
)

// Release builds populate these through -ldflags.
var version = "dev"
var revision = "unknown"
var auditRevision = "unknown"

func versionText() string {
	return fmt.Sprintf("incidentctl %s (%s/%s)\nTUI: %s\naudit-agent: %s\n", version, runtime.GOOS, runtime.GOARCH, revision, auditRevision)
}
