#!/usr/bin/env bash
# Build, verify and publish the release archives of RELEASE_VERSION, from the
# PUBLIC repository at its tag. Run it after scripts/publish.sh --commit and
# the push of main and v<version>.
#
#   scripts/release.sh          a dry run: every check, then the plan and the
#                               release notes; nothing is built or published
#   scripts/release.sh --go     do it
#
# Steps:
#   1. preflight: the tag v<version> is on the public repository, no GitHub
#      release of it exists yet, and the tag's tree holds that version and a
#      changelog entry; with --go also macOS, gh logged in, SIGN_IDENTITY and
#      NOTARY_PROFILE set, and no earlier output in bin/release/<version>
#   2. a depth-1 clone of the public repository at the tag, so the archives
#      carry exactly the public commit
#   3. make release there, signed and notarized
#   4. REQUIRE_SIGNATURE=1 make verify-release VERIFY_FLAGS=--rebuild there:
#      signatures, notarization, the tag's commit in --version, and a second
#      build with the same bytes
#   5. the eight files are copied to bin/release/<version>/ in this repository
#      (scripts/tap.sh compares the published assets with them)
#   6. gh release create v<version> on IZZY-Agency/sitboard with the eight
#      files and the changelog entry as notes
# It never touches the tap: scripts/tap.sh is the next, separate step.
#
# Environment: SIGN_IDENTITY and NOTARY_PROFILE (required for --go, never
# stored here), EXPECTED_TEAM_ID (optional, passed to verify-release).
# Rehearsals (scripts/release_test.sh): RELEASE_TEST_REMOTE=/abs/bare.git
# reads the public repository from a local bare repository instead of GitHub.
set -euo pipefail

usage="usage: scripts/release.sh [--go]"
GO=0
case "${1:-}" in
  "") ;;
  --go) GO=1 ;;
  *) echo "$usage" >&2; exit 2 ;;
esac
[ $# -le 1 ] || { echo "$usage" >&2; exit 2; }
fail() { echo "release FAILED: $*" >&2; exit 1; }
step() { echo "==> $*"; }
# --go builds and publishes for real: never against a stand-in.
if ((GO)) && [ -n "${RELEASE_TEST_REMOTE:-}" ]; then
  fail "--go never runs with RELEASE_TEST_REMOTE: the clone would come from a stand-in while the release goes to GitHub"
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$ROOT"
# Git ignores the user's global and system config and hooks here too, and gh
# targets the repository named below on github.com, nothing the environment says.
unset GIT_CONFIG GIT_CONFIG_PARAMETERS GIT_CONFIG_COUNT GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GH_HOST GH_REPO
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
REPO="github.com/IZZY-Agency/sitboard"
URL="https://$REPO.git"
if [ -n "${RELEASE_TEST_REMOTE:-}" ]; then
  case "$RELEASE_TEST_REMOTE" in /*) [ -d "$RELEASE_TEST_REMOTE" ] || fail "RELEASE_TEST_REMOTE is not a folder" ;; *) fail "RELEASE_TEST_REMOTE must be absolute" ;; esac
  URL="file://$RELEASE_TEST_REMOTE"
  step "REHEARSAL: the public repository is read from $RELEASE_TEST_REMOTE"
fi

VERSION="$(sed -n 's/^RELEASE_VERSION := \([^ ]*\)$/\1/p' Makefile)"
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "RELEASE_VERSION in the Makefile is '${VERSION:-missing}'"
TAG="v$VERSION"
OUT="$ROOT/bin/release/$VERSION"
FILES=()
for s in macos-arm64 macos-x86_64 linux-arm64 linux-x86_64; do
  FILES+=("sitboard-$VERSION-$s.tar.gz" "sitboard-$VERSION-$s.tar.gz.sha256")
done

# clean_make runs make for the release with nothing from the environment that
# could change what it builds or checks: no development switch, no make
# flags or extra makefiles (they act as command-line assignments), no Go
# flags, workspace, toolchain switch or user go env file. The release's own
# settings are passed as make arguments.
clean_make() {
  env -u PACKAGE_DEV -u PACKAGE_ALLOW_DIRTY -u COMMIT -u SOURCE_DATE_EPOCH -u VERIFY_FLAGS -u REQUIRE_SIGNATURE \
      -u MAKEFLAGS -u MAKEFILES -u MFLAGS -u GNUMAKEFLAGS -u MAKELEVEL \
      -u GOFLAGS -u GOWORK -u GOTOOLCHAIN -u GOPATH -u GOPROXY -u GONOSUMDB -u GONOSUMCHECK -u GOFIPS140 \
      GOENV=off make "$@"
}

WORK="$(mktemp -d)" || fail "could not create a scratch folder"
WORK="$(cd "$WORK" && pwd -P)"
case "$WORK" in /?*) ;; *) fail "bad scratch folder: $WORK" ;; esac
trap 'rm -rf "$WORK"' EXIT

# --- 1. preflight ---------------------------------------------------------------------
step "preflight for sitboard $VERSION ($TAG)"
needs=()
[ "$(uname -s)" = Darwin ] || needs+=("macOS (codesign, notarytool)")
[ -n "${SIGN_IDENTITY:-}" ] || needs+=("SIGN_IDENTITY (a Developer ID Application identity)")
[ -n "${NOTARY_PROFILE:-}" ] || needs+=("NOTARY_PROFILE (a notarytool keychain profile)")
[ ! -e "$OUT" ] || needs+=("no earlier output in $OUT (move it away)")
command -v gh >/dev/null 2>&1 || fail "gh is not on PATH"
gh auth status >/dev/null 2>&1 || needs+=("gh logged in (gh auth login)")

tag_line="$(git ls-remote "$URL" "refs/tags/$TAG")" || fail "could not read $URL"
[ -n "$tag_line" ] || fail "$TAG is not on the public repository: run scripts/publish.sh --commit and push it first"
if gh release view "$TAG" --repo "$REPO" >/dev/null 2>&1; then
  fail "the release $TAG already exists on $REPO (delete it first if it must be rebuilt)"
fi

step "clone $TAG"
git clone -q --depth 1 --branch "$TAG" "$URL" "$WORK/src" 2>/dev/null || fail "could not clone $TAG"
src_version="$(sed -n 's/^RELEASE_VERSION := \([^ ]*\)$/\1/p' "$WORK/src/Makefile")"
[ "$src_version" = "$VERSION" ] || fail "$TAG holds RELEASE_VERSION '${src_version:-missing}', not $VERSION"
awk -v v="$VERSION" '
  $0 == "## " v { on = 1; next }
  on && /^## /   { exit }
  on             { print }
' "$WORK/src/CHANGELOG.md" | sed -e '/./,$!d' > "$WORK/notes.md"
[ -s "$WORK/notes.md" ] || fail "CHANGELOG.md at $TAG has no '## $VERSION' entry"
[ -f "$WORK/src/scripts/package.sh" ] || fail "$TAG has no scripts/package.sh"

if ((GO == 0)); then
  step "dry run: the plan for --go"
  echo "    in a clone of $URL at $TAG:"
  echo "      make release SIGN_IDENTITY=\"\$SIGN_IDENTITY\" NOTARY_PROFILE=\"\$NOTARY_PROFILE\"   (environment cleared)"
  echo "      make verify-release REQUIRE_SIGNATURE=1 VERIFY_FLAGS=--rebuild"
  echo "    copy to $OUT:"
  printf '      %s\n' "${FILES[@]}"
  echo "    gh release create $TAG <the eight files> --repo $REPO --verify-tag --title \"sitboard $VERSION\" --notes-file <notes below>"
  echo "    then, by hand: PUBLISH_IDENT=\"Name <email>\" scripts/tap.sh <tap checkout>, review, push"
  step "release notes"
  sed 's/^/    /' "$WORK/notes.md"
  if ((${#needs[@]})); then
    step "--go would stop here; it needs:"
    printf '    - %s\n' "${needs[@]}"
  fi
  echo "dry run: nothing was built or published. Re-run with --go to do it."
  exit 0
fi

((${#needs[@]} == 0)) || { printf '    - %s\n' "${needs[@]}" >&2; fail "not ready (above)"; }

# --- 2 to 4. build and verify in the public clone ------------------------------------------
# The clone must be the tag's commit as the public repository itself says.
tag_commit="$(git ls-remote "$URL" "refs/tags/$TAG^{}" | awk '{ print $1; exit }')"
[ -n "$tag_commit" ] || tag_commit="$(awk '{ print $1; exit }' <<<"$tag_line")"
[ "$(git -C "$WORK/src" rev-parse HEAD)" = "$tag_commit" ] \
  || fail "the clone is at $(git -C "$WORK/src" rev-parse HEAD), not $TAG's commit $tag_commit on $URL"
step "make release in the clone of $TAG ($tag_commit)"
(cd "$WORK/src" && clean_make release SIGN_IDENTITY="$SIGN_IDENTITY" NOTARY_PROFILE="$NOTARY_PROFILE") || fail "make release failed"
step "verify-release: signatures, notarization, the tag's commit in --version, a second build"
(cd "$WORK/src" && clean_make verify-release REQUIRE_SIGNATURE=1 VERIFY_FLAGS=--rebuild) || fail "verify-release failed"

# --- 5. keep the eight files here -------------------------------------------------------------
mkdir -p "$(dirname "$OUT")"
mkdir "$OUT" 2>/dev/null || fail "$OUT appeared meanwhile; move it away"
ASSETS=()
for f in "${FILES[@]}"; do
  [ -s "$WORK/src/bin/release/$VERSION/$f" ] || fail "missing $f"
  cp "$WORK/src/bin/release/$VERSION/$f" "$OUT/$f"
  ASSETS+=("$OUT/$f")
done

# --- 6. the GitHub release --------------------------------------------------------------------
step "gh release create $TAG"
gh release create "$TAG" "${ASSETS[@]}" --repo "$REPO" --verify-tag \
  --title "sitboard $VERSION" --notes-file "$WORK/notes.md" || fail "gh release create failed"
step "released: https://github.com/$REPO/releases/tag/$TAG"
echo "    next, by hand: PUBLISH_IDENT=\"Name <email>\" scripts/tap.sh <tap checkout>, review, push"
