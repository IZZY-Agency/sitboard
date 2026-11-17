#!/usr/bin/env bash
# Build and pack the release archives of one version, one per platform:
#
#   sitboard-<version>-macos-arm64.tar.gz    sitboard-<version>-macos-x86_64.tar.gz
#   sitboard-<version>-linux-arm64.tar.gz    sitboard-<version>-linux-x86_64.tar.gz
#
# Each holds sitboard (0755), LICENSE, THIRD_PARTY_NOTICES.md and README.md at
# the top level and comes
# with a <file>.sha256 in `shasum -a 256` format (`<hex>  <file>`), so
# `shasum -a 256 -c <file>.sha256` (or `sha256sum -c`) checks a download.
#
#   scripts/package.sh <version> <outdir>
#
# `make release` builds the web page first and calls this with RELEASE_VERSION
# from the Makefile. The web page in internal/server/dist is embedded; the
# script refuses to pack a binary without it.
#
# The binaries are reproducible: CGO off, -trimpath, -buildvcs=false, stripped,
# the version and commit set by -ldflags. The archives are too: fixed entry
# order and mtime, uid/gid 0, no owner names, no extended attributes, no gzip
# timestamp. Two runs on one commit give the same bytes.
#
# Environment:
#   COMMIT          the commit printed by --version (default: git's short HEAD)
#   SOURCE_DATE_EPOCH
#                   mtime of the archive entries (default: the commit date of HEAD)
#   SIGN_IDENTITY   a Developer ID Application identity. On macOS each darwin
#                   binary is signed with it (hardened runtime, timestamp).
#                   Empty: no signing step (the Go linker still gives arm64
#                   binaries the ad hoc signature macOS requires).
#   NOTARY_PROFILE  a notarytool keychain profile. With SIGN_IDENTITY, each
#                   signed binary goes through scripts/notarize.sh.
#   PACKAGE_DEV=1   a development build: skip the release checks below
#                   (local tests only). Its archives are named
#                   sitboard-dev-<version>-... and it prints "sitboard
#                   dev-<version> (<commit>)", so it is never taken for a release.
#   PACKAGE_ALLOW_DIRTY=1
#                   build from a working tree with uncommitted changes (local
#                   tests only; a release is built from a clean tree)
#
# A release build (no PACKAGE_DEV) is made only in a clone of the public
# repository (origin is exactly its URL) whose HEAD is the tag v<version>, so
# the binaries print the public commit and nothing private can be packed.
set -euo pipefail

usage="usage: scripts/package.sh <version> <outdir>"
VERSION="${1:?$usage}"
OUTDIR_ARG="${2:?$usage}"
SIGN_IDENTITY="${SIGN_IDENTITY:-}"
NOTARY_PROFILE="${NOTARY_PROFILE:-}"
MODULE="github.com/IZZY-Agency/sitboard"

fail() { echo "package: $*" >&2; exit 1; }
step() { echo "==> $*"; }

# The output folder is the caller's: resolved before anything moves, created
# only once every check has passed.
case "$OUTDIR_ARG" in /*) OUTDIR="$OUTDIR_ARG" ;; *) OUTDIR="$PWD/$OUTDIR_ARG" ;; esac
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$ROOT"
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] || fail "bad version: '$VERSION' (want 1.2.3)"
for f in LICENSE THIRD_PARTY_NOTICES.md README.md cmd/sitboard/main.go; do
  [ -f "$f" ] || fail "$f is missing in $ROOT"
done
[ -s internal/server/dist/index.html ] || fail "no web page in internal/server/dist: run make web-build (make release does)"
if [ "${PACKAGE_DEV:-0}" != 1 ]; then
  origin="$(git config --get-all remote.origin.url 2>/dev/null || true)"
  [ "$origin" = "https://github.com/IZZY-Agency/sitboard.git" ] \
    || fail "a release is built in a clone of the public repository (origin here: '${origin:-none}'); PACKAGE_DEV=1 makes a local test build"
  git tag --points-at HEAD | grep -qx "v$VERSION" \
    || fail "HEAD is not the tag v$VERSION (tags here: '$(git tag --points-at HEAD | tr '\n' ' ')'); PACKAGE_DEV=1 makes a local test build"
fi
# A development build never looks like a release: its archives and its
# --version say dev-<version>.
SHOWN="$VERSION"
if [ "${PACKAGE_DEV:-0}" = 1 ]; then SHOWN="dev-$VERSION"; fi
if [ "${PACKAGE_ALLOW_DIRTY:-0}" != 1 ]; then
  [ -z "$(git status --porcelain)" ] || fail "the working tree is not clean: commit first, a release is built from a commit"
fi

host_os="$(uname -s)"
if [ -n "$NOTARY_PROFILE" ] && [ -z "$SIGN_IDENTITY" ]; then
  fail "NOTARY_PROFILE needs SIGN_IDENTITY (an unsigned binary cannot be notarized)"
fi
if [ -n "$SIGN_IDENTITY" ] && [ "$host_os" != Darwin ]; then
  fail "signing needs macOS (codesign); unset SIGN_IDENTITY for an unsigned build"
fi

COMMIT="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || true)}"
[[ "$COMMIT" =~ ^[0-9a-f]{7,40}$ ]] || fail "bad commit: '${COMMIT:-none}'"

epoch="${SOURCE_DATE_EPOCH:-}"
if [ -z "$epoch" ]; then epoch="$(git log -1 --format=%ct 2>/dev/null || true)"; fi
case "$epoch" in *[!0-9]*|"") fail "SOURCE_DATE_EPOCH must be a Unix timestamp, got '$epoch'" ;; esac
# `touch -t` wants [[CC]YY]MMDDhhmm[.SS]: format in UTC, apply in UTC.
if date -u -r "$epoch" +%Y >/dev/null 2>&1; then
  stamp="$(date -u -r "$epoch" +%Y%m%d%H%M.%S)"    # BSD date
else
  stamp="$(date -u -d "@$epoch" +%Y%m%d%H%M.%S)"   # GNU date
fi

tar_flavor="$(tar --version 2>/dev/null | head -1 || true)"
case "$tar_flavor" in
  *"GNU tar"*)
    TAR_FLAGS=(--format=ustar --owner=0 --group=0 --numeric-owner --no-xattrs) ;;
  *bsdtar*|*libarchive*)
    TAR_FLAGS=(--format ustar --uid 0 --gid 0 --numeric-owner --no-xattrs --no-acls --no-fflags)
    if [ "$host_os" = Darwin ]; then TAR_FLAGS+=(--no-mac-metadata); fi ;;
  *) fail "unrecognised tar (${tar_flavor:-no --version}); GNU tar or bsdtar is needed" ;;
esac
if command -v shasum >/dev/null 2>&1; then SHA=(shasum -a 256)
elif command -v sha256sum >/dev/null 2>&1; then SHA=(sha256sum)
else fail "neither shasum nor sha256sum is on PATH"
fi

mkdir -p "$OUTDIR" || fail "cannot create $OUTDIR"
OUTDIR="$(cd "$OUTDIR" && pwd -P)"
WORK="$(mktemp -d)" || fail "could not create a scratch folder"
WORK="$(cd "$WORK" && pwd -P)"
case "$WORK" in /?*) ;; *) fail "bad scratch folder: $WORK" ;; esac
trap 'rm -rf "$WORK"' EXIT
host_goos="$(go env GOOS)"
host_goarch="$(go env GOARCH)"
ldflags="-s -w -X $MODULE/internal/buildinfo.Version=$SHOWN -X $MODULE/internal/buildinfo.Commit=$COMMIT"

# "<GOOS>/<GOARCH>:<archive suffix>"
for t in darwin/arm64:macos-arm64 darwin/amd64:macos-x86_64 linux/arm64:linux-arm64 linux/amd64:linux-x86_64; do
  goos="${t%%/*}"
  rest="${t#*/}"
  goarch="${rest%%:*}"
  name="sitboard-$SHOWN-${rest#*:}"
  stage="$WORK/$name"
  mkdir -p "$stage"

  step "build $name ($goos/$goarch)"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -buildvcs=false -ldflags "$ldflags" -o "$stage/sitboard" ./cmd/sitboard

  if [ "$goos" = darwin ] && [ -n "$SIGN_IDENTITY" ]; then
    step "sign $name"
    codesign --force --timestamp --options runtime -s "$SIGN_IDENTITY" "$stage/sitboard"
    if [ -n "$NOTARY_PROFILE" ]; then
      step "notarize $name"
      bash scripts/notarize.sh "$stage/sitboard" "$NOTARY_PROFILE" "$name"
    fi
  fi

  # The native build must print the version it was built with.
  if [ "$goos" = "$host_goos" ] && [ "$goarch" = "$host_goarch" ]; then
    got="$("$stage/sitboard" --version)"
    [ "$got" = "sitboard $SHOWN ($COMMIT)" ] || fail "$name prints '$got', expected 'sitboard $SHOWN ($COMMIT)'"
  fi

  cp LICENSE README.md THIRD_PARTY_NOTICES.md "$stage/"
  chmod 0755 "$stage/sitboard"
  chmod 0644 "$stage/LICENSE" "$stage/README.md" "$stage/THIRD_PARTY_NOTICES.md"
  TZ=UTC touch -t "$stamp" "$stage/LICENSE" "$stage/README.md" "$stage/THIRD_PARTY_NOTICES.md" "$stage/sitboard"

  archive="$OUTDIR/$name.tar.gz"
  rm -f "$archive" "$archive.sha256"
  (cd "$stage" && tar "${TAR_FLAGS[@]}" -cf - LICENSE README.md THIRD_PARTY_NOTICES.md sitboard) | gzip -n -9 > "$archive"
  (cd "$OUTDIR" && "${SHA[@]}" "$name.tar.gz" > "$name.tar.gz.sha256")
done

step "release archives: $OUTDIR"
(cd "$OUTDIR" && cat sitboard-"$SHOWN"-*.tar.gz.sha256)
