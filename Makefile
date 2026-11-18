# sitboard. `make check` runs everything CI runs; `make build` produces bin/sitboard.
# Each step skips itself, and says so, when its input does not exist yet.

SHELL := /bin/bash
.DEFAULT_GOAL := build

GOLANGCI_VERSION := v2.15.0
GOLANGCI := $(shell command -v golangci-lint 2>/dev/null || echo go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION))

# RELEASE_VERSION is the one source of truth for the release version: `make
# release` stamps it into the binaries, scripts/publish.sh tags it, and
# CHANGELOG.md must open with its entry. A dev build (`make build`) prints
# `git describe` instead.
RELEASE_VERSION := 0.2.0
RELEASE_DIR := bin/release/$(RELEASE_VERSION)
# Published builds sign (and notarize) the macOS binaries; empty: unsigned.
SIGN_IDENTITY ?=
NOTARY_PROFILE ?=

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X github.com/IZZY-Agency/sitboard/internal/buildinfo.Version=$(VERSION) \
           -X github.com/IZZY-Agency/sitboard/internal/buildinfo.Commit=$(COMMIT)

HAS_WEB := $(wildcard web/package.json)
HAS_GEN := $(wildcard internal/contract/gen/main.go)
HAS_CMD := $(wildcard cmd/sitboard/main.go)

.PHONY: check build test lint web-deps web-check web-build go-build contract contract-check private private-all \
	notices notices-check release verify-release release-test release-test-linux print-version

check: lint test web-check contract-check notices-check private release-test
	@echo "make check: all steps passed"

build: web-build go-build

lint:
	$(GOLANGCI) run ./...

test:
	go test -race ./...

# PRIVATE_FLAGS: CI passes --no-denylist (it has no deny-list); locally the list is required.
private:
	bash scripts/check-private_test.sh
	bash scripts/check-private.sh $(PRIVATE_FLAGS)
	bash scripts/check-private.sh --history --scope=branch $(PRIVATE_FLAGS)

# private-all is the full scan of this repository: the tree, and the history
# of every ref (other worktrees' branches, the stash, notes, tags). make check
# scans this branch only; run this before every release. Before going public,
# also run scripts/check-private.sh --history --pull-refs (scripts/README.md).
private-all:
	bash scripts/check-private.sh $(PRIVATE_FLAGS)
	bash scripts/check-private.sh --history $(PRIVATE_FLAGS)

# THIRD_PARTY_NOTICES.md lists the licence of everything the binary carries
# besides sitboard's own code: Go, the Go modules in the build, the npm
# packages in the web bundle and the fonts. It is embedded in the binary
# (sitboard --licenses, /licenses.txt). notices-check fails when it is out of
# date, so nothing new ships without an entry.
notices: web-deps
	bash scripts/notices.sh

notices-check: web-deps
	bash scripts/notices.sh --check
	bash scripts/notices_test.sh

contract:
ifneq ($(HAS_GEN),)
	go run ./internal/contract/gen -out web/src/contract.gen.ts
else
	@echo "skip: contract generation (no internal/contract/gen/)"
endif

contract-check:
ifneq ($(HAS_GEN),)
	go run ./internal/contract/gen -check web/src/contract.gen.ts
else
	@echo "skip: contract freshness (no internal/contract/gen/)"
endif

ifneq ($(HAS_WEB),)
web/node_modules/.package-lock.json: web/package-lock.json
	npm --prefix web ci

web-deps: web/node_modules/.package-lock.json

web-check: web-deps
	npm --prefix web run check
	npm --prefix web run test --if-present

web-build: web-deps
	npm --prefix web run build
	@touch internal/server/dist/.keep
else
web-deps web-check web-build:
	@echo "skip: web steps (no web/package.json)"
endif

go-build:
ifneq ($(HAS_CMD),)
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/sitboard ./cmd/sitboard
else
	@echo "skip: binary build (no cmd/sitboard/)"
endif

# release builds the four release archives (macOS and Linux, arm64 and
# x86_64) with the web page embedded, into $(RELEASE_DIR). It runs only in a
# clean clone of the public repository at tag v$(RELEASE_VERSION);
# PACKAGE_DEV=1 makes a local test build anywhere. scripts/release.sh runs it
# with the signing identity; see scripts/README.md.
# notices-check first: a release never embeds a stale THIRD_PARTY_NOTICES.md.
release: notices-check web-build
	SIGN_IDENTITY="$(SIGN_IDENTITY)" NOTARY_PROFILE="$(NOTARY_PROFILE)" \
		bash scripts/package.sh $(RELEASE_VERSION) $(RELEASE_DIR)

# verify-release checks the archives in $(RELEASE_DIR) and runs those this
# machine can run; each must print this checkout's commit.
# REQUIRE_SIGNATURE=1 also checks the macOS signatures and notarization;
# VERIFY_FLAGS=--rebuild builds them again and compares the bytes.
verify-release:
	REQUIRE_SIGNATURE="$(REQUIRE_SIGNATURE)" bash scripts/verify-release.sh $(RELEASE_DIR) $(if $(filter 1,$(PACKAGE_DEV)),dev-)$(RELEASE_VERSION) $(shell git rev-parse --short HEAD) $(VERIFY_FLAGS)

release-test:
	bash scripts/release_test.sh

# release-test-linux runs the release tests and the private-material guard's
# tests on Linux (GNU grep, sed and friends; the public repository's CI runs on
# Linux), in a container, from a clone of what is committed: a worktree's .git
# file points at a host path the container cannot see, so the container gets a
# plain clone, and it clones that again (through git's own protocol, not by
# copying files across the mount) so nothing is written into the mount.
# Uncommitted changes are not tested. Ctrl-C stops the container: --init runs
# tini as its first process, and TINI_KILL_PROCESS_GROUP=1 has it pass the
# signal to every process in it. Needs docker.
LINUX_IMAGE ?= golang:1.27
release-test-linux:
	@command -v docker >/dev/null || { echo "release-test-linux: needs docker" >&2; exit 2; }
	@if [ -n "$$(git status --porcelain)" ]; then echo "release-test-linux: uncommitted changes are not tested; the clone sees commits only" >&2; fi
	@tmp="$$(mktemp -d)" && trap 'rm -rf "$$tmp"' EXIT && \
	git -c advice.detachedHead=false clone -q . "$$tmp/src" && \
	echo "release-test-linux: $$(git -C "$$tmp/src" log -1 --format='%h %s') in $(LINUX_IMAGE)" && \
	docker run --rm --init -e TINI_KILL_PROCESS_GROUP=1 -v "$$tmp/src":/src:ro $(LINUX_IMAGE) \
	  bash -c 'git config --global --add safe.directory "*" && git -c advice.detachedHead=false clone -q --no-local /src /w && cd /w \
	    && make release-test && bash scripts/check-private_test.sh'

print-version:
	@echo $(RELEASE_VERSION)
