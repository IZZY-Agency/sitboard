#!/usr/bin/env bash
# Write (or, with --check, verify) THIRD_PARTY_NOTICES.md: build the web page
# into a scratch folder with scripts/notices-bundle.mjs, which records every
# module and asset that went into it, then run internal/tools/notices on that.
#
#   scripts/notices.sh            make notices
#   scripts/notices.sh --check    make notices-check (part of make check)
set -euo pipefail

check=()
case "${1:-}" in
  "") ;;
  --check) check=(-check) ;;
  *) echo "usage: scripts/notices.sh [--check]" >&2; exit 2 ;;
esac

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$ROOT"
[ -d web/node_modules ] || { echo "notices: run make web-deps first" >&2; exit 1; }
WORK="$(mktemp -d)" || { echo "notices: mktemp failed" >&2; exit 1; }
WORK="$(cd "$WORK" && pwd -P)"
case "$WORK" in /?*) ;; *) echo "notices: bad scratch folder" >&2; exit 1 ;; esac
trap 'rm -rf "$WORK"' EXIT

node scripts/notices-bundle.mjs web "$WORK"
go run ./internal/tools/notices -bundle "$WORK" ${check[@]+"${check[@]}"}
