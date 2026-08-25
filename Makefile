SHELL := /bin/bash -o pipefail

# Prefer the highest reachable release tag. `git describe` alone can choose an
# older name when multiple lightweight tags point at the same commit.
VERSION ?= $(shell tag=$$(git tag --merged HEAD --sort=-version:refname 'v[0-9]*' 2>/dev/null | head -n1); \
	if [[ -n "$$tag" ]]; then git describe --tags --dirty --match "$$tag"; else echo dev; fi)
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
