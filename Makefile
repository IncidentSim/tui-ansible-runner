SHELL := /bin/sh

GO ?= go
BINARY ?= bin/incidentctl
CONFIG ?= incidentctl.json
COMMAND := ./cmd/incidentctl

.PHONY: help config build run test vet fmt check clean

help:
	@printf '%s\n' \
		'make config             create incidentctl.json from the example' \
		'make build              build the incidentctl binary' \
		'make run                build and start the TUI' \
		'make test               run Go tests' \
		'make vet                run go vet' \
		'make fmt                format Go sources' \
		'make check              run formatting, tests, vet and build' \
		'make clean              remove the local binary'

config:
	@test -f "$(CONFIG)" || cp incidentctl.example.json "$(CONFIG)"

build:
	@mkdir -p "$(dir $(BINARY))"
	$(GO) build -o "$(BINARY)" $(COMMAND)

run: build
	"$(BINARY)" -config "$(CONFIG)" $(ARGS)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

check: fmt test vet build

clean:
	rm -f "$(BINARY)"
