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
RELEASE_VERSION := 0.6.0
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
# The website's folder, named without a slash, as scripts/publish.sh does: the
# public repository has no website, and its tree never names that folder's
# paths. The steps that read it skip themselves there.
SITE_DIR := site
HAS_SITE := $(wildcard $(SITE_DIR)/package.json)

.PHONY: check build test lint web-deps web-check web-build go-build contract contract-check facts facts-confirm private private-all \
	notices notices-check release verify-release export-gates release-test release-test-linux test-tmux-linux print-version

check: lint test web-check site-sync contract-check notices-check private export-gates release-test
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

# audit looks the dependencies up in the vulnerability databases: govulncheck
# for the Go modules and this Go's standard library (only what the code
# reaches), npm audit for every package of the web page (bundled into the
# binary) and, where its folder exists, of the website (they build it), from
# the lockfiles. It needs the network, so it is not part of make check; run it
# before every release (scripts/README.md). Every step runs; any finding
# fails it.
GOVULNCHECK_VERSION := v1.10.0
.PHONY: audit
audit:
	@status=0; \
	echo "audit: govulncheck $(GOVULNCHECK_VERSION)"; go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./... || status=1; \
	echo "audit: npm audit, the web page"; npm --prefix web audit || status=1; \
	if [ -f $(SITE_DIR)/package.json ]; then echo "audit: npm audit, the website"; npm --prefix $(SITE_DIR) audit || status=1; \
	else echo "skip: npm audit of the website (no $(SITE_DIR) folder)"; fi; \
	if [ $$status -ne 0 ]; then echo "make audit: a step failed or found a vulnerability; see its output above" >&2; exit 1; fi; \
	echo "make audit: no known vulnerabilities"

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

# README.md's generated sections and the website's facts.gen.json are built from
# internal/facts/facts.yaml, the one list of what sitboard does, reads, sends
# and shows. `make test` fails when they are stale, when an anchor of a fact
# is gone or a value it states changed, when a page field is named by no
# fact, or when the code a fact rests on changed since it was confirmed:
# reread the fact, fix it if needed, then
#   make facts-confirm FACTS="<fact id> …"
facts:
	go run ./internal/facts/gen -write

facts-confirm:
ifeq ($(strip $(FACTS)),)
	$(error name the facts you reread: make facts-confirm FACTS="<fact id> …")
endif
	go run ./internal/facts/gen -confirm "$(FACTS)"

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

# site-sync: the website keeps copies of the page's design tokens, fonts
# stylesheet and favicon, which must stay byte for byte the page's (the
# website's own verify step checks the same files, but only after a site
# build). A plain compare, no build; it skips where the website's folder does
# not exist.
SITE_SHARED := src/styles/tokens.css src/styles/fonts.css public/favicon.svg
.PHONY: site-sync
site-sync:
ifneq ($(HAS_SITE),)
	@status=0; for f in $(SITE_SHARED); do \
	  cmp -s "web/$$f" "$(SITE_DIR)/$$f" || { echo "site-sync: the website's $$f differs from web/$$f; copy the page's" >&2; status=1; }; \
	done; \
	if [ $$status -eq 0 ]; then echo "site-sync: the website's copies match the page's ($(SITE_SHARED))"; fi; \
	exit $$status
else
	@echo "skip: site sync (no $(SITE_DIR) folder)"
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

# export-gates runs publish.sh's staging and fast gates (file kinds, sizes,
# ignore rules, docs/ and website pointers, check-private from the staged copy)
# on the tracked files, so a refused export fails at the pull request, not at
# release time. It has no target: no public checkout, no new repository, no
# network, no commit. The clean-copy make check stays release-only. PRIVATE_FLAGS
# may be empty or --no-denylist.
export-gates:
	PUBLISH_GATES_ONLY=1 PRIVATE_FLAGS="$(PRIVATE_FLAGS)" bash scripts/publish.sh

release-test:
	bash scripts/release_test.sh

# release-test-linux runs the release tests and the private-material guard's
# tests on Linux (GNU grep, sed and friends; the public repository's CI runs on
# Linux), in a container, from a clone of what is committed: a worktree's .git
# file points at a host path the container cannot see, so the container gets a
# plain clone, and it clones that again (through git's own protocol, not by
# copying files across the mount) so nothing is written into the mount.
# Uncommitted changes are not tested. The host clone uses --no-hardlinks: a
# plain local clone hard-links the loose objects, so the container would read
# files that are the same inodes as the live repository's, which other
# worktrees write, repack and prune, and git inside the container would die
# with "unable to open loose object ... Permission denied" (ticket #70); copies
# belong to the clone alone. Ctrl-C stops the container: --init runs
# tini as its first process, and TINI_KILL_PROCESS_GROUP=1 has it pass the
# signal to every process in it. Needs docker.
LINUX_IMAGE ?= golang:1.27
release-test-linux:
	@command -v docker >/dev/null || { echo "release-test-linux: needs docker" >&2; exit 2; }
	@if [ -n "$$(git status --porcelain)" ]; then echo "release-test-linux: uncommitted changes are not tested; the clone sees commits only" >&2; fi
	@tmp="$$(mktemp -d)" && trap 'rm -rf "$$tmp"' EXIT && \
	git -c advice.detachedHead=false clone -q --no-hardlinks . "$$tmp/src" && \
	echo "release-test-linux: $$(git -C "$$tmp/src" log -1 --format='%h %s') in $(LINUX_IMAGE)" && \
	docker run --rm --init -e TINI_KILL_PROCESS_GROUP=1 -v "$$tmp/src":/src:ro $(LINUX_IMAGE) \
	  bash -c 'git config --global --add safe.directory "*" && git -c advice.detachedHead=false clone -q --no-local /src /w && cd /w \
	    && make release-test && bash scripts/check-private_test.sh'

# test-tmux-linux runs the tmux jump against a real tmux (TestRealTmux: a
# server of its own on a private socket, clients attached through script(1)),
# on Linux in a container, from a clone of what is committed, as
# release-test-linux does; it never touches a tmux server on this machine.
# Not part of `make check`: it needs docker and the network (apt-get).
test-tmux-linux:
	@command -v docker >/dev/null || { echo "test-tmux-linux: needs docker" >&2; exit 2; }
	@if [ -n "$$(git status --porcelain)" ]; then echo "test-tmux-linux: uncommitted changes are not tested; the clone sees commits only" >&2; fi
	@tmp="$$(mktemp -d)" && trap 'rm -rf "$$tmp"' EXIT && \
	git -c advice.detachedHead=false clone -q --no-hardlinks . "$$tmp/src" && \
	echo "test-tmux-linux: $$(git -C "$$tmp/src" log -1 --format='%h %s') in $(LINUX_IMAGE)" && \
	docker run --rm --init -e TINI_KILL_PROCESS_GROUP=1 -v "$$tmp/src":/src:ro $(LINUX_IMAGE) \
	  bash -c 'export DEBIAN_FRONTEND=noninteractive && apt-get update -qq >/dev/null && apt-get install -y -qq tmux bsdutils >/dev/null \
	    && git config --global --add safe.directory "*" && git -c advice.detachedHead=false clone -q --no-local /src /w && cd /w \
	    && tmux -V && go test -count=1 -v -run TestRealTmux ./internal/adapters/tmux 2>&1 | tee /tmp/out \
	    && grep -q "^--- PASS: TestRealTmux" /tmp/out'

print-version:
	@echo $(RELEASE_VERSION)
