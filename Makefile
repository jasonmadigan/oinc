SHELL := /bin/bash -o pipefail

VERSION ?= $(shell git describe --tags --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.buildVersion=$(VERSION) -s -w

.PHONY: build
build:
	go build -ldflags="$(LDFLAGS)" -o bin/oinc ./cmd/oinc

.PHONY: install
install:
	go install -ldflags="$(LDFLAGS)" ./cmd/oinc

.PHONY: fmt
fmt:
	go fmt ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: clean
clean:
	rm -rf bin/

.DEFAULT_GOAL := build
