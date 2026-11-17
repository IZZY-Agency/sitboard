#!/usr/bin/env bash
# Check the release archives of one version in a folder (make verify-release):
#
#   scripts/verify-release.sh <dir> <version> <commit> [--rebuild]
#
# For each of the four archives: the .sha256 file names it and matches it, and
# it holds exactly LICENSE, README.md, THIRD_PARTY_NOTICES.md and an executable
# sitboard. Every archive
# this machine can run (its own platform, and macOS x86_64 under Rosetta on
# Apple Silicon) is unpacked and must print exactly
# "sitboard <version> (<commit>)", the commit the release was built from.
# The others are listed as not run. With REQUIRE_SIGNATURE=1 (macOS only),
# both macOS binaries must carry a Developer ID Application signature with the
# hardened runtime and be notarized (codesign --check-notarization and spctl;
# the ticket is looked up online). EXPECTED_TEAM_ID, when set, is the team id
# the signature must name; no team is written in this repository.
# --rebuild builds the archives a second time, unsigned, from this checkout
# (scripts/package.sh, so the same release checks apply) and requires the
# same bytes: all four when the archives are unsigned, the two Linux ones
# when REQUIRE_SIGNATURE=1 (a signature changes the macOS binaries).
# Exit 0 when every check passed and at least one archive ran.
set -uo pipefail

usage="usage: scripts/verify-release.sh <dir> <version> <commit> [--rebuild]"
DIR="${1:?$usage}"
VERSION="${2:?$usage}"
COMMIT="${3:?$usage}"
REBUILD=0
case "${4:-}" in "") ;; --rebuild) REBUILD=1 ;; *) echo "$usage" >&2; exit 2 ;; esac
[[ "$COMMIT" =~ ^[0-9a-f]{7,40}$ ]] || { echo "verify-release: bad commit '$COMMIT'" >&2; exit 2; }
[ -d "$DIR" ] || { echo "verify-release: no folder $DIR" >&2; exit 2; }
if [ -n "${EXPECTED_TEAM_ID:-}" ] && ! [[ "$EXPECTED_TEAM_ID" =~ ^[A-Z0-9]{10}$ ]]; then
  echo "verify-release: EXPECTED_TEAM_ID must be a 10-character Apple team id" >&2; exit 2
fi

WORK="$(mktemp -d)" || { echo "verify-release: mktemp failed" >&2; exit 2; }
WORK="$(cd "$WORK" && pwd -P)"
case "$WORK" in /?*) ;; *) echo "verify-release: bad scratch folder" >&2; exit 2 ;; esac
trap 'rm -rf "$WORK"' EXIT

if command -v shasum >/dev/null 2>&1; then SHA=(shasum -a 256); else SHA=(sha256sum); fi
os="$(uname -s)"; arch="$(uname -m)"
rosetta() { [ "$os/$arch" = Darwin/arm64 ] && /usr/bin/arch -x86_64 /usr/bin/true 2>/dev/null; }
runnable() {  # <suffix>
  case "$1/$os/$arch" in
    macos-arm64/Darwin/arm64|macos-x86_64/Darwin/x86_64) return 0 ;;
    linux-arm64/Linux/aarch64|linux-arm64/Linux/arm64|linux-x86_64/Linux/x86_64) return 0 ;;
    macos-x86_64/Darwin/arm64) rosetta ;;
    *) return 1 ;;
  esac
}

fail=0; ran=0
for suffix in macos-arm64 macos-x86_64 linux-arm64 linux-x86_64; do
  name="sitboard-$VERSION-$suffix.tar.gz"
  archive="$DIR/$name"
  if [ ! -s "$archive" ] || [ ! -s "$archive.sha256" ]; then
    echo "[FAIL] $name: archive or .sha256 missing"; fail=1; continue
  fi
  want="$(awk '{ print $1; exit }' "$archive.sha256")"
  named="$(awk '{ sub(/^\*/, "", $2); print $2; exit }' "$archive.sha256")"
  got="$("${SHA[@]}" "$archive" | cut -d' ' -f1)"
  if [ "$named" != "$name" ] || [ "$got" != "$want" ]; then
    echo "[FAIL] $name: checksum does not match its .sha256"; fail=1; continue
  fi
  listing="$(tar -tzf "$archive" 2>/dev/null | LC_ALL=C sort | tr '\n' ' ')"
  if [ "$listing" != "LICENSE README.md THIRD_PARTY_NOTICES.md sitboard " ]; then
    echo "[FAIL] $name: holds '$listing', want LICENSE README.md THIRD_PARTY_NOTICES.md sitboard"; fail=1; continue
  fi
  x="$WORK/$suffix"
  mkdir -p "$x"
  tar -xzf "$archive" -C "$x" || { echo "[FAIL] $name: does not unpack"; fail=1; continue; }
  [ -x "$x/sitboard" ] || { echo "[FAIL] $name: sitboard is not executable"; fail=1; continue; }
  if runnable "$suffix"; then
    out="$("$x/sitboard" --version 2>&1)"
    if [ "$out" = "sitboard $VERSION ($COMMIT)" ]; then
      echo "[ok]   $name: $out"; ran=$((ran + 1))
    else
      echo "[FAIL] $name: --version printed '$out', want 'sitboard $VERSION ($COMMIT)'"; fail=1
    fi
  else
    echo "[ok]   $name: checksum and contents (not run on $os/$arch)"
  fi
  if [ "${REQUIRE_SIGNATURE:-0}" = 1 ] && [[ "$suffix" == macos-* ]]; then
    if [ "$os" != Darwin ]; then
      echo "[FAIL] $name: REQUIRE_SIGNATURE=1 needs macOS (codesign, spctl)"; fail=1; continue
    fi
    bin="$x/sitboard"
    info="$(codesign -dvv "$bin" 2>&1 || true)"
    problems=""
    codesign --verify --strict "$bin" 2>/dev/null || problems="$problems; the signature does not verify"
    grep -q "Authority=Developer ID Application" <<<"$info" || problems="$problems; not signed by a Developer ID Application certificate"
    grep -q "flags=.*runtime" <<<"$info" || problems="$problems; no hardened runtime"
    if [ -n "${EXPECTED_TEAM_ID:-}" ]; then
      grep -qx "TeamIdentifier=$EXPECTED_TEAM_ID" <<<"$info" || problems="$problems; not signed by team EXPECTED_TEAM_ID"
    fi
    # Notarized: the ticket is found (online for a bare binary) and
    # Gatekeeper's verdict names a notarized Developer ID.
    codesign -vvvv -R="notarized" --check-notarization "$bin" >/dev/null 2>&1 || problems="$problems; not notarized"
    verdict="$(spctl --assess --type install -v "$bin" 2>&1 || true)"
    grep -q "source=Notarized Developer ID" <<<"$verdict" || problems="$problems; Gatekeeper does not accept it as notarized"
    if [ -z "$problems" ]; then
      echo "[ok]   $name: Developer ID signature, hardened runtime, notarized"
    else
      echo "[FAIL] $name: ${problems#; }"; fail=1
    fi
  fi
done

if ((REBUILD)); then
  ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
  # A development build is named dev-<version>; package.sh takes the version.
  if SIGN_IDENTITY="" NOTARY_PROFILE="" COMMIT="$COMMIT" bash "$ROOT/scripts/package.sh" "${VERSION#dev-}" "$WORK/rebuild" > "$WORK/rebuild.log" 2>&1; then
    for suffix in macos-arm64 macos-x86_64 linux-arm64 linux-x86_64; do
      if [ "${REQUIRE_SIGNATURE:-0}" = 1 ] && [[ "$suffix" == macos-* ]]; then
        echo "[ok]   rebuild: $suffix not compared (signed)"
        continue
      fi
      name="sitboard-$VERSION-$suffix.tar.gz"
      a="$("${SHA[@]}" "$DIR/$name" | cut -d' ' -f1)"
      b="$("${SHA[@]}" "$WORK/rebuild/$name" | cut -d' ' -f1)"
      if [ -n "$a" ] && [ "$a" = "$b" ]; then
        echo "[ok]   rebuild: $name is byte for byte the same"
      else
        echo "[FAIL] rebuild: $name differs from a second build"; fail=1
      fi
    done
  else
    tail -5 "$WORK/rebuild.log"
    echo "[FAIL] rebuild: scripts/package.sh failed"; fail=1
  fi
fi

if ((ran == 0)); then
  echo "[FAIL] no archive could be run on $os/$arch"; fail=1
fi
if ((fail)); then
  echo "verify-release: FAIL"
  exit 1
fi
echo "verify-release: PASS ($ran archive(s) run)"
