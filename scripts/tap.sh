#!/usr/bin/env bash
# Point the Homebrew tap at a PUBLISHED sitboard release: download the four
# archives and their .sha256 files from the GitHub release, require each
# archive to match its published .sha256 AND the .sha256 of the local build in
# bin/release/<version>/, write Formula/sitboard.rb from
# scripts/formula/sitboard.rb.tmpl, add the README install line and table row
# when missing, run `brew style`, and commit in the tap checkout. Pushing is
# manual; the script prints the exact command.
#
#   scripts/tap.sh [<tap-checkout>]        default: ../homebrew-tap
#
# The version is RELEASE_VERSION in the Makefile. Run it after the GitHub
# release v<version> exists with its assets. Committing needs
# PUBLISH_IDENT="Name <email>" (see scripts/publish.sh).
#
# The checkout must be the root of a clone whose origin is the tap, with no
# content filters, URL rewrites, includes or attributes, and its HEAD the
# tap's published main (a checkout that is merely behind is fast-forwarded
# first), so no unpublished commit rides along with the push. A formula that
# `brew style` rejects is taken back out: the checkout is left as it was.
#
# Rehearsals (scripts/release_test.sh), never for a real update. They print
# no push command:
#   TAP_TEST_REMOTE=/abs       a stand-in for the tap: the checkout's origin
#                              must be exactly this local folder (never the
#                              tap's URL)
#   TAP_TEST_ASSETS_DIR=/abs   read the "published" archives from that folder
#                              instead of downloading them (needs
#                              TAP_TEST_REMOTE)
#   TAP_NO_STYLE=1             skip `brew style`
# All git calls ignore the user's global and system git config and hooks.
set -euo pipefail

# Once the checkout is being written to, any way out but a commit puts it
# back as it was: an EXIT trap, so an abort under set -e restores it too.
restore=0
WORK=""
cleanup() {
  if [ "$restore" = 1 ]; then
    tgit checkout -q HEAD -- README.md 2>/dev/null || true
    if tgit cat-file -e "HEAD:$FORMULA" 2>/dev/null; then
      tgit checkout -q HEAD -- "$FORMULA" || true
    else
      rm -f "$TAP/$FORMULA"
    fi
    echo "tap: the tap checkout was restored" >&2
  fi
  [ -z "$WORK" ] || rm -rf "$WORK"
}
trap cleanup EXIT
fail() { echo "tap FAILED: $*" >&2; exit 1; }
step() { echo "==> $*"; }

# The checkout is resolved against the caller's folder, before anything moves.
TAP_ARG="${1:-}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
[ -n "$TAP_ARG" ] || TAP_ARG="$ROOT/../homebrew-tap"
[ -d "$TAP_ARG" ] || fail "$TAP_ARG is not a folder"
TAP="$(cd "$TAP_ARG" && pwd -P)"
cd "$ROOT"

REPO="IZZY-Agency/sitboard"
TAP_URL="https://github.com/IZZY-Agency/homebrew-tap.git"
FORMULA="Formula/sitboard.rb"
# shellcheck disable=SC2016 # the backticks are Markdown
README_ROW='| `sitboard` (formula) | A live local board of your AI coding agents, their pull requests and checks (macOS and Linux) | [IZZY-Agency/sitboard](https://github.com/IZZY-Agency/sitboard) |'
README_INSTALL='brew install izzy-agency/tap/sitboard'

unset GIT_CONFIG GIT_CONFIG_PARAMETERS GIT_CONFIG_COUNT GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
while IFS= read -r v; do unset "$v"; done < <(env | sed -n 's/^\(GIT_CONFIG_[A-Za-z0-9_]*\)=.*/\1/p')
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
tgit() {
  git -C "$TAP" -c core.attributesFile=/dev/null -c core.hooksPath=/dev/null \
    -c core.fsmonitor=false -c core.untrackedCache=false -c core.quotePath=false -c gc.auto=0 \
    -c i18n.commitEncoding=UTF-8 -c commit.gpgSign=false -c tag.gpgSign=false "$@"
}

VERSION="$(sed -n 's/^RELEASE_VERSION := \([^ ]*\)$/\1/p' Makefile)"
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "RELEASE_VERSION in the Makefile is '${VERSION:-missing}'"
LOCAL="$ROOT/bin/release/$VERSION"
DL="https://github.com/$REPO/releases/download/v$VERSION"

ident="${PUBLISH_IDENT:-}"
ident_re='^([^<>]*[^<> ]) <([^<>@ ]+@[^<>@ ]+)>$'
[[ "$ident" =~ $ident_re ]] || fail "committing needs PUBLISH_IDENT=\"Name <email>\""
export GIT_AUTHOR_NAME="${BASH_REMATCH[1]}" GIT_AUTHOR_EMAIL="${BASH_REMATCH[2]}"
export GIT_COMMITTER_NAME="${BASH_REMATCH[1]}" GIT_COMMITTER_EMAIL="${BASH_REMATCH[2]}"

ASSETS_DIR="${TAP_TEST_ASSETS_DIR:-}"
TEST_REMOTE="${TAP_TEST_REMOTE:-}"
FETCH_URL="$TAP_URL"
ORIGIN_WANT="$TAP_URL"
if [ -n "$TEST_REMOTE" ]; then
  case "$TEST_REMOTE" in /*) [ -d "$TEST_REMOTE" ] || fail "TAP_TEST_REMOTE is not a folder" ;; *) fail "TAP_TEST_REMOTE must be absolute" ;; esac
  FETCH_URL="$TEST_REMOTE"
  ORIGIN_WANT="$TEST_REMOTE"
  step "REHEARSAL: the tap is stood in for by $FETCH_URL"
fi
if [ "${TAP_NO_STYLE:-0}" = 1 ] && [ -z "$TEST_REMOTE" ]; then
  fail "TAP_NO_STYLE=1 is for rehearsals only: a real tap update must pass brew style"
fi
if [ -n "$ASSETS_DIR" ]; then
  [ -n "$TEST_REMOTE" ] || fail "TAP_TEST_ASSETS_DIR needs TAP_TEST_REMOTE: a rehearsal never commits in the real tap checkout"
  case "$ASSETS_DIR" in /*) [ -d "$ASSETS_DIR" ] || fail "TAP_TEST_ASSETS_DIR is not a folder" ;; *) fail "TAP_TEST_ASSETS_DIR must be absolute" ;; esac
  step "REHEARSAL: the published assets are read from $ASSETS_DIR"
fi

top="$(tgit rev-parse --show-toplevel 2>/dev/null)" || fail "$TAP is not a git checkout"
[ "$top" -ef "$TAP" ] || fail "$TAP is inside a checkout ($top), not the checkout root"
[ "$(tgit config --get-all remote.origin.url || true)" = "$ORIGIN_WANT" ] \
  || fail "$TAP is not the tap checkout (its origin must be $ORIGIN_WANT)"
[ -z "$(tgit config --get-regexp '^(filter\.|url\.|include\.|includeif\.)' || true)" ] \
  || fail "the tap checkout configures content filters, URL rewrites or includes"
pushurls="$(tgit config --get-regexp '^remote\..*\.pushurl$' || true)"
[ -z "$pushurls" ] || fail "the tap checkout sets a push URL ($pushurls): a plain push would go there; remove it"
if [ -n "$TEST_REMOTE" ]; then
  real="$(tgit config --get-regexp '^remote\..*\.url$' | awk -v u="$TAP_URL" '$2 == u' || true)"
  [ -z "$real" ] || fail "a rehearsal never runs in a checkout with a remote that is the tap ($real)"
fi
attrs="$(tgit rev-parse --git-path info/attributes)"
case "$attrs" in /*) ;; *) attrs="$TAP/$attrs" ;; esac
[ ! -s "$attrs" ] || fail "the tap checkout has info/attributes"
[ -z "$(tgit status --porcelain)" ] || fail "the tap checkout is not clean"
[ -d "$LOCAL" ] || fail "no local build in $LOCAL: the archives must be the ones built for this release (make release)"

step "reading the tap history from $FETCH_URL"
remote_main="$(tgit ls-remote "$FETCH_URL" refs/heads/main | awk '$2 == "refs/heads/main" { print $1; exit }')" \
  || fail "could not read $FETCH_URL"
[ -n "$remote_main" ] || fail "$FETCH_URL has no main branch"
tgit fetch -q --no-tags "$FETCH_URL" "+refs/heads/main:refs/remotes/origin/main" || fail "could not fetch main"
[ "$(tgit rev-parse -q --verify 'refs/remotes/origin/main^{commit}')" = "$remote_main" ] || fail "fetched main does not match the remote"
head="$(tgit rev-parse -q --verify 'HEAD^{commit}' || true)"
if [ "$head" != "$remote_main" ]; then
  if [ -n "$head" ] && tgit merge-base --is-ancestor "$head" "$remote_main"; then
    step "fast-forwarding the tap checkout to ${remote_main:0:7}"
    tgit merge -q --ff-only "$remote_main" || fail "could not fast-forward the tap checkout"
  else
    fail "HEAD (${head:-unborn}) is not the tap's main ($remote_main): it carries unpublished commits; reset first"
  fi
fi

# WORK is set only once checked: the EXIT trap removes it.
w="$(mktemp -d)" || fail "could not create a scratch folder"
w="$(cd "$w" && pwd -P)" || fail "could not enter the scratch folder"
case "$w" in /?*) [ -d "$w" ] || fail "bad scratch folder: $w" ;; *) fail "bad scratch folder: $w" ;; esac
WORK="$w"

fetch_asset() {  # <file>: into $WORK
  if [ -n "$ASSETS_DIR" ]; then
    cp "$ASSETS_DIR/$1" "$WORK/$1" 2>/dev/null || fail "no $1 in $ASSETS_DIR"
  else
    curl -fsSL --proto '=https' -o "$WORK/$1" "$DL/$1" || fail "could not download $DL/$1 (is the release published?)"
  fi
}
sha_of() { if command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1"; else sha256sum "$1"; fi | cut -d' ' -f1; }
digest_in() {  # <.sha256 file> <archive name>: the digest, after checking the name
  local want named
  want="$(awk '{ print $1; exit }' "$1")"
  named="$(awk '{ sub(/^\*/, "", $2); print $2; exit }' "$1")"
  [[ "$want" =~ ^[0-9a-f]{64}$ ]] || fail "$1 holds no sha256"
  [ "$named" = "$2" ] || fail "$1 names '$named', not $2"
  echo "$want"
}
# verified <archive name>: the digest of the published archive, which must
# match its published .sha256 and the local build's .sha256. It runs inside
# $(...), where set -e does not hold: every step's status is checked here, and
# the caller checks the function's.
verified() {
  local file="$1" got pub loc
  fetch_asset "$file" || return 1
  fetch_asset "$file.sha256" || return 1
  got="$(sha_of "$WORK/$file")" && [ -n "$got" ] || fail "could not hash $file"
  pub="$(digest_in "$WORK/$file.sha256" "$file")" || return 1
  [ "$got" = "$pub" ] || fail "the published $file hashes to $got but its published .sha256 says $pub"
  [ -s "$LOCAL/$file.sha256" ] || fail "no local $LOCAL/$file.sha256"
  loc="$(digest_in "$LOCAL/$file.sha256" "$file")" || return 1
  [ "$got" = "$loc" ] || fail "the published $file hashes to $got but the local build says $loc: the release assets are not this build"
  echo "$got"
}

step "verifying the published assets of v$VERSION"
s1="$(verified "sitboard-$VERSION-macos-arm64.tar.gz")" || exit 1
s2="$(verified "sitboard-$VERSION-macos-x86_64.tar.gz")" || exit 1
s3="$(verified "sitboard-$VERSION-linux-arm64.tar.gz")" || exit 1
s4="$(verified "sitboard-$VERSION-linux-x86_64.tar.gz")" || exit 1

step "writing $FORMULA"
bash "$ROOT/scripts/render-formula.sh" "$VERSION" "$s1" "$s2" "$s3" "$s4" > "$WORK/formula.rb" || fail "could not render the formula"
restore=1
mkdir -p "$TAP/Formula"
cp "$WORK/formula.rb" "$TAP/$FORMULA"

# add_after_last <file> <regex> <line> <what>: insert after the last matching line.
add_after_last() {
  local file="$1" regex="$2" line="$3" n
  n="$(grep -nE -- "$regex" "$file" | tail -1 | cut -d: -f1)"
  [ -n "$n" ] || fail "the tap README has no $4; add '$line' by hand"
  awk -v n="$n" -v new="$line" 'NR == n { print; print new; next } { print }' "$file" > "$file.tmp" && mv "$file.tmp" "$file"
}
readme="$TAP/README.md"
[ -f "$readme" ] || fail "no README.md in the tap"
grep -qF -- "$README_INSTALL" "$readme" || add_after_last "$readme" '^brew install ' "$README_INSTALL" "brew install line"
# shellcheck disable=SC2016
grep -qF -- '`sitboard` (formula)' "$readme" || add_after_last "$readme" '^\| `' "$README_ROW" "package table"

if [ "${TAP_NO_STYLE:-0}" != 1 ]; then
  command -v brew >/dev/null 2>&1 || fail "brew is not on PATH (brew style must pass)"
  step "brew style"
  HOMEBREW_NO_AUTO_UPDATE=1 brew style "$TAP/$FORMULA" || fail "brew style rejected $FORMULA"
fi

if [ -z "$(tgit status --porcelain)" ]; then
  restore=0
  step "sitboard is already at $VERSION in the tap: nothing to commit"
  exit 0
fi
tgit add -- "$FORMULA" README.md
tgit commit -q --no-gpg-sign -m "sitboard $VERSION" || fail "commit failed"
restore=0
who="$(tgit log -1 --format='%an <%ae>|%cn <%ce>|%G?')"
[ "$who" = "$ident|$ident|N" ] || fail "the tap commit carries $who (reset the tap before pushing)"
[ "$(tgit rev-parse 'HEAD^')" = "$remote_main" ] || fail "the new commit does not sit on the published main (reset the tap)"
new="$(tgit rev-parse HEAD)"
if [ -n "$TEST_REMOTE" ]; then
  step "committed ${new:0:7} in the stand-in tap checkout (REHEARSAL: no push command)"
  exit 0
fi
step "committed ${new:0:7} in the tap; review it, then push exactly that commit with:"
printf '    git -C %q push --no-follow-tags %s %s:refs/heads/main\n' "$TAP" "$TAP_URL" "$new"
