GO ?= go
ifeq ($(OS),Windows_NT)
SHELL := cmd.exe
.SHELLFLAGS := /C
BINARY ?= bin/incidentctl.exe
RUN_BINARY = $(subst /,\,$(BINARY))
else
SHELL := /bin/sh
BINARY ?= bin/incidentctl
RUN_BINARY = $(BINARY)
endif
CONFIG ?= incidentctl.json
COMMAND := ./cmd/incidentctl

.PHONY: help config build run test vet fmt check clean

help:
	@echo make config             create incidentctl.json from the example
	@echo make build              build the incidentctl binary
	@echo make run                build and start the TUI
	@echo make test               run Go tests
	@echo make vet                run go vet
	@echo make fmt                format Go sources
	@echo make check              run formatting, tests, vet and build
	@echo make clean              remove the local binary

config:
ifeq ($(OS),Windows_NT)
	@if not exist "$(subst /,\,$(CONFIG))" copy /Y incidentctl.example.json "$(subst /,\,$(CONFIG))" >NUL
else
	@test -f "$(CONFIG)" || cp incidentctl.example.json "$(CONFIG)"
endif

build:
	$(GO) generate ./internal/auditbinary
	$(GO) build -o "$(BINARY)" $(COMMAND)

run: build
	"$(RUN_BINARY)" -config "$(CONFIG)" $(ARGS)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

check: fmt test vet build

clean:
ifeq ($(OS),Windows_NT)
	@if exist "$(RUN_BINARY)" del /Q "$(RUN_BINARY)"
else
	rm -f "$(BINARY)"
endif
