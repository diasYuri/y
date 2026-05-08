# Convenience Makefile for y. The authoritative implementation lives in
# scripts/{build,check,release}.sh; this file just exposes shorter aliases.

SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c

GO ?= go

# Default flavor and binary; override on the command line, e.g.
#   make build BINARY=y-pods FLAVOR=full
BINARY ?= y
FLAVOR ?= standard
VERSION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)

.PHONY: all check fmt vet test test-all build build-all matrix release clean help models

all: check build

help:
	@echo "Targets:"
	@echo "  check       gofmt + go vet + go test + tagged tests"
	@echo "  fmt         gofmt -l (fails on diffs)"
	@echo "  vet         go vet ./..."
	@echo "  test        go test ./..."
	@echo "  test-all    go test ./... with every feature_* tag"
	@echo "  build       build BINARY=$(BINARY) FLAVOR=$(FLAVOR) for host"
	@echo "  build-all   build every binary at FLAVOR=$(FLAVOR) for host"
	@echo "  matrix      build BINARY=$(BINARY) for the cross-platform matrix"
	@echo "  release     scripts/release.sh VERSION=$(VERSION) FLAVOR=$(FLAVOR)"
	@echo "  clean       remove build artefacts, caches and stray scratch"

check:
	@./scripts/check.sh all

fmt:
	@./scripts/check.sh fmt

vet:
	@./scripts/check.sh vet

test:
	@./scripts/check.sh test

test-all:
	@./scripts/check.sh test-all

build:
	@./scripts/build.sh --binary $(BINARY) --flavor $(FLAVOR)

build-all:
	@./scripts/build.sh --binary all --flavor $(FLAVOR)

matrix:
	@./scripts/build.sh --binary $(BINARY) --flavor $(FLAVOR) --matrix

release:
	@./scripts/release.sh --version $(VERSION) --flavor $(FLAVOR)

clean:
	@chmod -R u+w .gocache .gomodcache 2>/dev/null || true
	@rm -rf bin dist bin-test dist-test .gocache .gomodcache .extract .charmdl
	@rm -f y bubbles.mod test_mod.mod

# models regenerates the per-provider models_gen.go files from each
# subpackage's models.json. Equivalent to `go generate ./pkg/providers/...`.
models:
	@$(GO) generate ./pkg/providers/...
