#!/usr/bin/env bash
# Export a snapshot of sitboard into a checkout of the public repository
# (github.com/IZZY-Agency/sitboard). The working repository stays private.
# The snapshot is built from an ALLOWLIST of TRACKED paths, read from the
# index, so nothing that is not named below, and nothing uncommitted, can
# reach it.
#
#   scripts/publish.sh <public-checkout>
#       stage the snapshot, run the gates, print the tree and the changes;
#       commit nothing
#
#   scripts/publish.sh <public-checkout> --commit
#       a RELEASE: commit "sitboard <version>" (body: that version's
#       CHANGELOG.md entry) and tag v<version> (annotated, unsigned). The
#       version is RELEASE_VERSION in the Makefile, the one source of truth.
#
#   scripts/publish.sh <public-checkout> --update "<subject>"
#       between releases (README, CI, tooling): commit with that subject, no
#       tag. Release tags stay on the commit the shipped build came from.
#
# Refused before anything is touched:
#   - in the export set, every file must be a regular file of a kind listed
#     for its top-level folder (KINDS below: adding a new kind of file is a
#     deliberate edit there), at most MAX_BYTES unless named in SIZE_EXCEPTIONS;
#     and none may be, ignoring case: inside a .claude, .codex or docs folder,
#     in the website's top-level folder, .env*, a key or certificate store, a
#     credentials file (.npmrc, .netrc, .pypirc, id_rsa and kin), a database,
#     an editor swap or backup file, CLAUDE.md or AGENTS.md; tracked files
#     that .gitignore ignores are refused too
#   - a target that is, contains or sits inside the private repository
#     (compared by file identity, so another spelling of the path does not
#     help), shares its git folder, or holds its history (a clone of it)
#   - a target with uncommitted changes, index flags that hide changes
#     (assume-unchanged, skip-worktree), untracked or ignored files, staged
#     changes this script did not make, a push URL, content filters, URL
#     rewrites, includes, attributes, grafts, replace refs or a pending
#     operation
#   - for --update, a subject naming a private commit or object, a private
#     branch or tag, or a docs/ path; for --commit, an export without the
#     project tiles and side rail the README and CHANGELOG describe
#
# Gates, all fail closed, run in a scratch repository BEFORE the target is
# written to, so a refused snapshot leaves nothing in the public checkout:
#   - the staged files are exactly the exported files, blob for blob
#   - no pointer to a docs/ file the snapshot does not carry, and no reference
#     to the website's folder (relative forms included; a link to
#     https://sitboard.com is fine)
#   - scripts/check-private.sh in tree mode WITH the deny-list (a missing or
#     empty deny-list stops the export)
#   - a clean copy, in its own new repository, must pass
#     `make check PRIVATE_FLAGS=--no-denylist` and `make build`
# Committing: the message is scanned with check-private.sh --text first; after
# the commit, check-private.sh --history WITH the deny-list runs on the
# target, and a commit that fails it is undone, its reflog expired and its
# objects pruned.
#
# Committing needs PUBLISH_IDENT="Name <email>": the one identity the public
# commit and tag may carry, set through the environment (which outranks every
# git config source) and verified afterwards. It is never stored here.
# Pushing is deliberately manual; the script prints the exact command, naming
# the new commit by its id.
#
# The target must be the root of a clone of the public repository (origin is
# exactly the public URL, no push URL) whose HEAD is the published main (or
# unborn while the repository is empty), with no local-only tags or other
# branches. Its tracked files are DELETED and replaced.
#
# Rehearsals (scripts/release_test.sh), never for a real publish. Neither
# marks last-published nor prints a push command, and neither accepts a
# checkout with any remote or push URL that is the public repository's:
#   PUBLISH_TEST_TARGET=1     a target with any other origin; the private tree
#                             may be dirty; stage only: a commit is refused
#   PUBLISH_TEST_REMOTE=/abs  a stand-in for the public repository: the
#                             target's origin must be exactly this local
#                             folder, and committing runs as for real
#   PUBLISH_TEST_INTERRUPT_AFTER=N  (with either) stop the write into the
#                             target after N files, as Ctrl-C would
# All git calls ignore the user's global and system git config and hooks.
set -euo pipefail

usage='usage: publish.sh <public-checkout> [--commit | --update "<subject>"]'
TARGET_ARG="${1:?$usage}"
MODE="${2:-}"
ARG="${3:-}"
fail() { echo "publish FAILED: $*" >&2; exit 1; }
step() { echo "==> $*"; }
case "$MODE" in
  "") [ $# -le 1 ] || { echo "$usage" >&2; exit 2; } ;;
  --commit) [ $# -eq 2 ] || { echo "--commit takes no argument; the version comes from the Makefile" >&2; exit 2; } ;;
  --update) [ -n "$ARG" ] || { echo "--update needs a commit subject" >&2; exit 2; } ;;
  *) echo "publish: unknown option: $MODE" >&2; echo "$usage" >&2; exit 2 ;;
esac
TEST_TARGET="${PUBLISH_TEST_TARGET:-0}"
TEST_REMOTE="${PUBLISH_TEST_REMOTE:-}"
REHEARSAL=0
if [ "$TEST_TARGET" = 1 ] || [ -n "$TEST_REMOTE" ]; then REHEARSAL=1; fi

# The target is resolved against the caller's folder, before anything moves.
[ -d "$TARGET_ARG" ] || fail "$TARGET_ARG is not a directory"
TARGET="$(cd "$TARGET_ARG" && pwd -P)"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$ROOT"

# No configuration may reach git through the environment.
unset GIT_CONFIG GIT_CONFIG_PARAMETERS GIT_CONFIG_COUNT GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE \
      GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_NAMESPACE GIT_COMMON_DIR
while IFS= read -r v; do unset "$v"; done < <(env | sed -n 's/^\(GIT_CONFIG_[A-Za-z0-9_]*\)=.*/\1/p')
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1

CANONICAL_URL="https://github.com/IZZY-Agency/sitboard.git"
FETCH_URL="$CANONICAL_URL"
if [ -n "$TEST_REMOTE" ]; then
  case "$TEST_REMOTE" in
    /*) [ -d "$TEST_REMOTE" ] || fail "PUBLISH_TEST_REMOTE must be a local directory" ;;
    *) fail "PUBLISH_TEST_REMOTE must be an absolute local path" ;;
  esac
  FETCH_URL="$TEST_REMOTE"
  step "REHEARSAL: the public repository is stood in for by $FETCH_URL"
fi

# Everything the public tree consists of. Paths must be tracked; a directory
# exports every tracked file under it.
ALLOWLIST=(
  cmd internal web scripts Makefile go.mod go.sum README.md LICENSE THIRD_PARTY_NOTICES.md CHANGELOG.md
  .gitignore
  # The tool's workflows, by name: the website and its workflow are never
  # published here.
  .github/workflows/ci.yml
  # Beyond the agreed list, needed so the public tree passes `make check`:
  testdata       # fixtures the adapter tests read
  test           # the end-to-end test package
  .golangci.yml  # the lint configuration `make lint` uses
  notices.go notices_test.go  # the root package that embeds LICENSE and the notices
)

# The kinds of file each top-level folder may export: "<folder>:<extensions>"
# and, for files without an extension, "<folder>=<exact names>". "." is the
# repository root, whose files are the allowlist's own names. Anything else
# stops the run and names the file.
KINDS=(
  ".:.go .md .mod .sum .yml"
  ".=Makefile LICENSE .gitignore"
  ".github:.yml"
  "cmd:.go"
  "internal:.go .json .txt"
  "internal=.keep"
  "scripts:.md .mjs .sh .tmpl"
  "test:.go"
  "testdata:.json .jsonl .md"
  "web:.css .html .js .json .mjs .svelte .svg .ts .txt .woff2"
)
# No exported file is larger than this, unless named here.
MAX_BYTES=1048576
SIZE_EXCEPTIONS=()
# The only binary files the export may hold (git's test: a NUL byte in the
# first 8000 bytes). Any other binary content stops the run, whatever its name.
BINARY_ALLOWED=(
  web/src/fonts/jetbrains-mono-latin-wght-normal.woff2
  web/src/fonts/jetbrains-mono-latin-ext-wght-normal.woff2
  web/src/fonts/space-grotesk-latin-wght-normal.woff2
  web/src/fonts/space-grotesk-latin-ext-wght-normal.woff2
)
# The website's top-level folder: the website is not published with the tool.
# This file never spells it as a path, so the reference gate passes on it.
SITE_DIR="site"
# README.md and CHANGELOG.md describe project tiles and the side rail: a
# release (--commit) needs the grouping UI that provides them.
RELEASE_NEEDS=(web/src/components/ProjectTiles.svelte web/src/components/ProjectRail.svelte web/src/data/groups.ts)

# Private-side git: hooks off too (global and system config are off above).
pgit() { git -c core.hooksPath=/dev/null "$@"; }
# Target-side git: none of the target's own settings may run a program, cache
# what it has seen, or change what a commit records.
tgit() {
  git -C "$TARGET" -c core.attributesFile=/dev/null -c core.hooksPath=/dev/null \
    -c core.fsmonitor=false -c core.untrackedCache=false -c core.quotePath=false -c gc.auto=0 \
    -c i18n.commitEncoding=UTF-8 -c commit.gpgSign=false -c tag.gpgSign=false -c tag.forceSignAnnotated=false "$@"
}

# The private commit the public tree was last built from: a local lightweight
# tag, for `git diff last-published HEAD` before the next publish.
mark_published() {
  if [ "$REHEARSAL" = 1 ]; then
    echo "    REHEARSAL: last-published not moved"
    return
  fi
  pgit tag -f last-published HEAD > /dev/null
  echo "    marked $(pgit rev-parse --short HEAD) as last-published"
}
push_cmd() {  # [tag]
  if [ "$REHEARSAL" = 1 ]; then
    echo "    REHEARSAL: no push command"
    return
  fi
  printf '    push with: git -C %q push --no-follow-tags %s %s:refs/heads/main%s\n' \
    "$TARGET" "$CANONICAL_URL" "$(tgit rev-parse HEAD)" "${1:+ refs/tags/$1}"
}

WORK="$(mktemp -d)" || fail "could not create a scratch folder"
WORK="$(cd "$WORK" && pwd -P)"
case "$WORK" in /?*) ;; *) fail "bad scratch folder: $WORK" ;; esac
writing=0
# restore_target puts the target's files and index back as they were before
# the write step began: whatever it wrote is removed, the old files checked out.
restore_target() {
  if [ -s "$WORK/exported.stage" ]; then
    while IFS= read -r f; do rm -f -- "${TARGET:?}/${f:?}"; done < "$WORK/exported.stage"
  fi
  tgit read-tree "$pre_tree" && tgit checkout-index -a -f
  find "$TARGET" -mindepth 1 -type d -empty -not -path "$TARGET/.git" -not -path "$TARGET/.git/*" -delete 2>/dev/null || true
  tgit prune --expire=now 2>/dev/null || true   # the blobs the write had stored
  echo "publish: interrupted while writing; the target was put back as it was" >&2
}
on_exit() {
  if [ "$writing" = 1 ]; then restore_target; fi
  rm -rf "${WORK:?}"
}
trap on_exit EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# --- preflight: the source ---------------------------------------------------------
if [ "$TEST_TARGET" != 1 ]; then
  [ -z "$(pgit status --porcelain --untracked-files=no)" ] || fail "the private working tree is not clean"
fi
[ -f scripts/check-private.sh ] || fail "scripts/check-private.sh is missing"
version="$(pgit show HEAD:Makefile | sed -n 's/^RELEASE_VERSION := \([^ ]*\)$/\1/p')"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "RELEASE_VERSION in the Makefile is '${version:-missing}', not a release version"

ident=""
if [ -n "$MODE" ]; then
  ident="${PUBLISH_IDENT:-}"
  ident_re='^([^<>]*[^<> ]) <([^<>@ ]+@[^<>@ ]+)>$'
  [[ "$ident" =~ $ident_re ]] \
    || fail "committing needs PUBLISH_IDENT=\"Name <email>\", the identity of public commits"
  export GIT_AUTHOR_NAME="${BASH_REMATCH[1]}" GIT_AUTHOR_EMAIL="${BASH_REMATCH[2]}"
  export GIT_COMMITTER_NAME="${BASH_REMATCH[1]}" GIT_COMMITTER_EMAIL="${BASH_REMATCH[2]}"
fi

for p in "${ALLOWLIST[@]}"; do
  pgit ls-files --error-unmatch -- "$p" > /dev/null 2>&1 || fail "allowlisted path is not tracked: $p"
done

# kind_ok <path>: the file is of a kind listed for its top-level folder.
kind_ok() {
  local f="$1" top base ext k
  case "$f" in */*) top="${f%%/*}" ;; *) top="." ;; esac
  base="${f##*/}"
  ext=""
  case "$base" in ?*.*) ext=".${base##*.}" ;; esac
  for k in "${KINDS[@]}"; do
    case "$k" in
      "$top:"*) [ -n "$ext" ] && [[ " ${k#*:} " == *" $ext "* ]] && return 0 ;;
      "$top="*) [[ " ${k#*=} " == *" $base "* ]] && return 0 ;;
    esac
  done
  return 1
}

# The export set: "<mode> <blob>\t<path>" per tracked file, NUL-separated.
pgit ls-files -s -z -- "${ALLOWLIST[@]}" > "$WORK/export"
bad=""
while IFS= read -r -d '' entry; do
  mode="${entry%% *}"
  rest="${entry#* }"
  blob="${rest%% *}"
  f="${entry#*$'\t'}"
  lc="$(printf '%s' "$f" | tr '[:upper:]' '[:lower:]')"
  base="${lc##*/}"
  case "$mode" in 100644|100755) ;; *) bad="$bad$f: not a regular file (symlink or submodule)"$'\n'; continue ;; esac
  case "/$lc/" in */.claude/*|*/.codex/*|*/docs/*) bad="$bad$f: inside a .claude, .codex or docs folder"$'\n' ;; esac
  case "$lc" in "$SITE_DIR"/*) bad="$bad$f: part of the website, which is not published here"$'\n' ;; esac
  case "$base" in
    .env*|*.pem|*.key|*.p12|*.pfx|*.jks|*.keystore|*.crt|*.cer|.npmrc|.netrc|.pypirc|.pgpass|\
    *id_rsa*|*id_dsa*|*id_ecdsa*|*id_ed25519*|*.pem.*|*.key.*|*.sqlite|*.sqlite3|*.sqlite-*|*.db|*.db-*|\
    *.swp|*.swo|*~|.ds_store|claude.md|agents.md)
      bad="$bad$f: a private kind of file"$'\n' ;;
  esac
  kind_ok "$f" || bad="$bad$f: not a kind of file this folder exports (KINDS in scripts/publish.sh)"$'\n'
  all="$(pgit cat-file blob "$blob" | head -c 8000 | wc -c || true)"
  nonul="$(pgit cat-file blob "$blob" | head -c 8000 | tr -d '\000' | wc -c || true)"
  if [ "$all" != "$nonul" ] && [[ " ${BINARY_ALLOWED[*]} " != *" $f "* ]]; then
    bad="$bad$f: binary content (BINARY_ALLOWED in scripts/publish.sh)"$'\n'
  fi
  size="$(pgit cat-file -s "$blob")"
  if [ "$size" -gt "$MAX_BYTES" ] && [[ " ${SIZE_EXCEPTIONS[*]-} " != *" $f "* ]]; then
    bad="$bad$f: $size bytes, more than $MAX_BYTES (SIZE_EXCEPTIONS in scripts/publish.sh)"$'\n'
  fi
done < "$WORK/export"
[ -z "$bad" ] || { printf '%s' "$bad" >&2; fail "forbidden files in the export set (above)"; }
ignored="$(pgit ls-files -ci --exclude-standard -- "${ALLOWLIST[@]}")"
[ -z "$ignored" ] || { echo "$ignored" >&2; fail "tracked files that .gitignore ignores are in the export set (above)"; }

if [ "$MODE" = --commit ]; then
  missing=""
  for need in "${RELEASE_NEEDS[@]}"; do
    pgit ls-files --error-unmatch -- "$need" > /dev/null 2>&1 || missing="$missing $need"
  done
  [ -z "$missing" ] || fail "the README and CHANGELOG describe project tiles and the side rail, but the export lacks:$missing (merge the grouping UI first)"
fi

# --- preflight: the target -----------------------------------------------------------
target_top="$(git -C "$TARGET" rev-parse --show-toplevel 2>/dev/null)" || fail "$TARGET is not a git checkout"
[ "$target_top" -ef "$TARGET" ] || fail "$TARGET is inside a checkout ($target_top), not the checkout root"
root_top="$(pgit rev-parse --show-toplevel)"
# within <a> <b>: a is b or lies below it, by file identity at every level.
within() {
  local d="$1"
  while :; do
    [ "$d" -ef "$2" ] && return 0
    if [ "$d" = / ] || [ "$d" = . ]; then return 1; fi
    d="$(dirname "$d")"
  done
}
within "$TARGET" "$root_top" && fail "the target is inside the private repository ($root_top): this would delete its files"
within "$root_top" "$TARGET" && fail "the private repository ($root_top) is inside the target: this would delete its files"
[ "$(tgit rev-parse --path-format=absolute --git-common-dir)" -ef "$(pgit rev-parse --path-format=absolute --git-common-dir)" ] \
  && fail "the target shares the private repository's git folder (a worktree of it): this would delete its files"
for c in $(pgit rev-parse HEAD) $(pgit rev-list --max-parents=0 HEAD); do
  if tgit cat-file -e "$c^{commit}" 2>/dev/null; then
    fail "the target holds the private repository's history (a clone of it?): refusing to delete its files"
  fi
done

# Nothing in the target's own config may rewrite content or reroute the
# remote, and no operation may be pending (it would add a parent or an author).
filters="$(tgit config --get-regexp '^(filter\.|url\.|include\.|includeif\.)' || true)"
[ -z "$filters" ] || fail "the target configures content filters, URL rewrites or includes"
pushurls="$(tgit config --get-regexp '^remote\..*\.pushurl$' || true)"
[ -z "$pushurls" ] || fail "the target sets a push URL ($pushurls): a plain push would go there; remove it"
if [ "$REHEARSAL" = 1 ]; then
  real="$(tgit config --get-regexp '^remote\..*\.url$' | awk -v u="$CANONICAL_URL" '$2 == u' || true)"
  [ -z "$real" ] || fail "a rehearsal never runs in a checkout with a remote that is the public repository ($real)"
fi
gpath() {
  local g
  g="$(tgit rev-parse --git-path "$1")"
  case "$g" in /*) ;; *) g="$TARGET/$g" ;; esac
  echo "$g"
}
[ ! -s "$(gpath info/attributes)" ] || fail "the target has info/attributes"
[ ! -e "$(gpath info/grafts)" ] || fail "the target has grafts"
[ ! -e "$(gpath shallow)" ] || fail "the target is a shallow clone"
[ -z "$(tgit replace -l)" ] || fail "the target has replace refs"
for state in MERGE_HEAD CHERRY_PICK_HEAD REVERT_HEAD BISECT_LOG rebase-merge rebase-apply sequencer; do
  [ ! -e "$(gpath "$state")" ] || fail "the target has a pending operation ($state)"
done

# The target's working state: nothing uncommitted, no index flag hiding a
# change, no stray files, and staged changes only when they are exactly what
# an earlier stage-only run left.
marker="$(gpath sitboard-publish-staged)"
hidden="$(tgit ls-files -v | grep -E '^([a-z]|S) ' || true)"
[ -z "$hidden" ] || { echo "$hidden" >&2; fail "the target has files flagged assume-unchanged or skip-worktree (above): their changes would be lost; clear the flags first"; }
changed="$(tgit diff --name-only)"
[ -z "$changed" ] || { echo "$changed" >&2; fail "the target has uncommitted changes to tracked files (above): commit or discard them first"; }
if ! tgit diff --cached --quiet; then
  [ -f "$marker" ] && [ "$(cat "$marker")" = "$(tgit write-tree)" ] \
    || fail "the target has staged changes that publish.sh did not make: commit or discard them first"
fi
status_all="$(tgit -c status.showUntrackedFiles=all status --porcelain --ignored --untracked-files=all)" \
  || fail "could not read the target status"
strays="$(printf '%s\n' "$status_all" | grep -E '^(\?\?|!!) ' || true)"
[ -z "$strays" ] || { echo "$strays" >&2; fail "the public checkout has untracked or ignored files"; }

rrefs="$WORK/remote-refs"
: > "$rrefs"
origin_urls="$(tgit config --get-all remote.origin.url || true)"
if [ "$TEST_TARGET" = 1 ]; then
  [ -z "$MODE" ] || fail "PUBLISH_TEST_TARGET=1 is for stage-only rehearsals: it never commits (rehearse a commit with PUBLISH_TEST_REMOTE)"
  step "REHEARSAL target (remote not checked): $TARGET"
else
  if [ -n "$TEST_REMOTE" ]; then
    [ "$origin_urls" = "$TEST_REMOTE" ] \
      || fail "a rehearsal commits only in a checkout whose origin is the stand-in $TEST_REMOTE (origin here: '${origin_urls:-none}')"
  else
    [ "$origin_urls" = "$CANONICAL_URL" ] \
      || fail "origin of $TARGET is '${origin_urls:-none}', not the public repository $CANONICAL_URL: refusing to delete its files"
  fi
  step "reading the public history"
  tgit ls-remote "$FETCH_URL" > "$rrefs" || fail "could not read $FETCH_URL"
  remote_main="$(awk '$2 == "refs/heads/main" { print $1 }' "$rrefs")"
  head="$(tgit rev-parse -q --verify 'HEAD^{commit}' || true)"
  if [ -n "$remote_main" ]; then
    # Compared with the remote's own answer; the tracking ref is updated only
    # after the gates, so a refused export changes nothing here.
    [ "$head" = "$remote_main" ] || fail "HEAD (${head:-unborn}) is not the public main ($remote_main): reset to it or drop local commits first"
  else
    [ -z "$(awk '$2 ~ /^refs\/heads\// { print }' "$rrefs")" ] || fail "the remote has branches but no main"
    [ -z "$head" ] || fail "the public repository is empty, but the target already has commits"
  fi
  # Tags carry objects too: every local tag must exist on the remote, as is.
  while IFS=' ' read -r oid ref; do
    [ -n "$ref" ] || continue
    grep -qxF -- "$oid"$'\t'"$ref" "$rrefs" || fail "local tag $ref is not on the remote as is: delete it first"
  done < <(tgit for-each-ref --format='%(objectname) %(refname)' refs/tags)
  others="$(tgit for-each-ref --format='%(refname)' refs/heads | grep -vx 'refs/heads/main' || true)"
  [ -z "$others" ] || fail "the target has other local branches: $others"
fi

# An --update subject is free text: it may not name a private commit or
# object, a private branch or tag, or a docs/ path.
if [ "$MODE" = --update ]; then
  # docs/ inside a link to the website is fine; anywhere else it is not.
  bare_subject="$(sed -E 's#https://sitboard\.com/[^[:space:]]*##g' <<<"$ARG")"
  case "$bare_subject" in *docs/*) fail "the subject names a docs/ path, which is not published" ;; esac
  # Hex runs of 6 or more, and every 7-character window of a longer run.
  for h in $(grep -oE '[0-9A-Fa-f]{6,}' <<<"$ARG" || true); do
    cands="$h"
    i=0
    while [ $((i + 7)) -le ${#h} ]; do cands="$cands ${h:i:7}"; i=$((i + 1)); done
    for c in $cands; do
      if pgit cat-file -e "$c^{object}" 2>/dev/null; then fail "the subject names $c, an object in the private repository"; fi
    done
  done
  # Private ref names, ignoring case. A name counts when it holds "-" or "/"
  # or is longer than 8 characters, so an ordinary word does not.
  ref_names() {
    pgit for-each-ref --format='%(refname:short)' refs/heads refs/tags refs/remotes
    pgit for-each-ref --format='%(refname:short)' refs/remotes | sed -n 's#^[^/]*/##p'
  }
  while IFS= read -r name; do
    [ -n "$name" ] || continue
    case "$name" in main|HEAD|origin|origin/HEAD|origin/main|last-published) continue ;; esac
    grep -qF $'\t'"refs/tags/$name" "$rrefs" && continue   # already public
    case "$name" in *-*|*/*) ;; *) [ ${#name} -gt 8 ] || continue ;; esac
    if grep -qiwF -- "$name" <<<"$ARG"; then fail "the subject names '$name', a private branch or tag"; fi
  done < <(ref_names | sort -u)
fi

tag="v$version"
body=""
if [ "$MODE" = --commit ]; then
  changelog="$(pgit show HEAD:CHANGELOG.md)" || fail "could not read CHANGELOG.md from HEAD"
  body="$(printf '%s\n' "$changelog" | awk -v v="$version" '
    $0 == "## " v { on = 1; next }
    on && /^## /   { exit }
    on             { print }
  ' | sed -e '/./,$!d')"
  [ -n "$body" ] || fail "CHANGELOG.md has no '## $version' entry (or it is empty)"
  if tgit rev-parse -q --verify "refs/tags/$tag" > /dev/null \
     || awk -v r="refs/tags/$tag" '$2 == r || $2 == r "^{}" { f = 1 } END { exit !f }' "$rrefs"; then
    fail "$tag is already released in the public repository: bump RELEASE_VERSION, or use --update for a change between releases"
  fi
fi

step "exporting sitboard $version from $(pgit rev-parse --short HEAD)"

# --- stage in a scratch repository and run every gate there ---------------------------------
# Nothing reaches the target until all gates passed.
STAGE="$WORK/stage"
git init -q -b main "$STAGE"
sgit() { git -C "$STAGE" -c core.hooksPath=/dev/null -c core.quotePath=false "$@"; }
# write_tree <dir> <git function>: the export set's files into <dir>, its
# index built from the same blobs (every stored blob must be the private one).
write_tree() {
  local dir="$1" g="$2" mode rest blob f written n=0
  : > "$WORK/index-info"
  : > "$WORK/exported"
  while IFS= read -r -d '' entry; do
    n=$((n + 1))
    # Rehearsals only: stop the target write after N files, as Ctrl-C would.
    if [ "$g" = tgit ] && [ "$REHEARSAL" = 1 ] && [ -n "${PUBLISH_TEST_INTERRUPT_AFTER:-}" ] && [ "$n" -gt "$PUBLISH_TEST_INTERRUPT_AFTER" ]; then
      kill -INT $$
    fi
    mode="${entry%% *}"
    rest="${entry#* }"
    blob="${rest%% *}"
    f="${entry#*$'\t'}"
    mkdir -p -- "$dir/$(dirname "$f")"
    pgit cat-file blob "$blob" > "$dir/$f" || fail "could not read $f from the index"
    if [ "$mode" = 100755 ]; then chmod 755 "$dir/$f"; else chmod 644 "$dir/$f"; fi
    written="$("$g" hash-object -w --no-filters -- "$f")" || fail "could not store $f"
    [ "$written" = "$blob" ] || fail "$f was not stored as the private blob"
    printf '%s %s\t%s\0' "$mode" "$blob" "$f" >> "$WORK/index-info"
    printf '%s\n' "$f" >> "$WORK/exported"
  done < "$WORK/export"
  "$g" read-tree --empty || fail "could not reset the index"
  "$g" update-index -z --index-info < "$WORK/index-info" || fail "could not build the index"
  LC_ALL=C sort "$WORK/exported" > "$WORK/exported.sorted"
  "$g" ls-files -z | tr '\0' '\n' | LC_ALL=C sort > "$WORK/staged.sorted"
  cmp -s "$WORK/exported.sorted" "$WORK/staged.sorted" \
    || { diff "$WORK/exported.sorted" "$WORK/staged.sorted" >&2 || true; fail "the staged files are not exactly the exported files"; }
}
write_tree "$STAGE" sgit
cp "$WORK/exported" "$WORK/exported.stage"   # what the target write may create

# No pointer to a docs/ file the snapshot does not carry: a path that starts
# docs/ (after any ./ or ../) at a word boundary; inside a URL it is not one.
dead=""
while IFS= read -r hit; do
  ref="${hit#*:*:}"
  ref="docs/${ref#*docs/}"
  [ -e "$STAGE/$ref" ] || dead="$dead${hit%%:*}:$(cut -d: -f2 <<<"$hit"): $ref is not exported"$'\n'
done < <(cd "$STAGE" && LC_ALL=C grep -rIonE --exclude-dir=.git '(^|[^A-Za-z0-9._/-])(\./|\.\./)*docs/[A-Za-z0-9._/-]*[A-Za-z0-9_-]' . \
           | sed 's|^\./||' || true)
[ -z "$dead" ] || { printf '%s' "$dead" >&2; fail "the public tree points at docs/ files it does not carry: say the fact where it is used"; }
# No reference to the website's folder (with or without a leading / or any
# ./ or ../; inside a URL such as https://host/<folder>/ it is not one).
site_refs="$(cd "$STAGE" && LC_ALL=C grep -rIonE --exclude-dir=.git "(^|[^A-Za-z0-9._/-])(\\./|\\.\\./|/)*$SITE_DIR/" . | sed 's|^\./||' || true)"
[ -z "$site_refs" ] || { echo "$site_refs" >&2; fail "the public tree refers to the website's folder (above): the website is not published here"; }

step "gate: scripts/check-private.sh with the deny-list"
(cd "$STAGE" && bash "$ROOT/scripts/check-private.sh") \
  || fail "the public tree failed check-private (above); fix it in the private repository"

step "gate: a clean copy passes make check and make build"
build="$WORK/build"
mkdir "$build"
sgit checkout-index -a --prefix="$build/" || fail "could not copy the staged tree"
git -C "$build" init -q -b main
git -C "$build" add -A
git -C "$build" -c core.hooksPath=/dev/null -c user.name=publish-gate -c user.email=gate@example.com \
  -c commit.gpgsign=false commit -q -m "snapshot" || fail "could not commit the clean copy"
if ! (cd "$build" && env -u CI make check PRIVATE_FLAGS=--no-denylist && make build) > "$WORK/build.log" 2>&1; then
  tail -40 "$WORK/build.log" >&2
  fail "the exported tree does not pass make check / make build (log above): something it needs is not exported"
fi
tail -3 "$WORK/build.log" | sed 's/^/    /'

# The commit message is scanned before it exists anywhere.
msg="$WORK/msg"
if [ "$MODE" = --update ]; then
  printf '%s\n' "$ARG" > "$msg"
elif [ "$MODE" = --commit ]; then
  printf 'sitboard %s\n\n%s\n' "$version" "$body" > "$msg"
fi
if [ -n "$MODE" ]; then
  step "gate: the commit message, with the deny-list"
  bash "$ROOT/scripts/check-private.sh" --text "$msg" || fail "the commit message failed check-private (above)"
fi

# --- only now: replace the public tree -------------------------------------------------
if [ -n "${remote_main:-}" ]; then
  tgit fetch -q --no-tags "$FETCH_URL" "+refs/heads/main:refs/remotes/origin/main" || fail "could not fetch main"
  [ "$(tgit rev-parse -q --verify 'refs/remotes/origin/main^{commit}')" = "$remote_main" ] || fail "fetched main does not match the remote"
fi
step "writing the snapshot into $TARGET"
# If anything stops the write (an error, Ctrl-C, a kill), the target's files
# and index are put back as they were.
pre_tree="$(tgit write-tree)"
writing=1
tgit ls-files -z | (cd "$TARGET" && xargs -0 rm -f --)
find "$TARGET" -mindepth 1 -type d -empty -not -path "$TARGET/.git" -not -path "$TARGET/.git/*" -delete
write_tree "$TARGET" tgit
writing=0

count="$(tgit ls-files | wc -l | tr -d ' ')"
step "public tree: $count files"
tgit ls-files | sed 's/^/    /'
step "changes against the previous snapshot (git status --short)"
changes="$(tgit status --short)"
if [ -z "$changes" ]; then
  echo "    (none: the public tree already matches)"
else
  printf '%s\n' "$changes" | sed 's/^/    /'
fi

if [ -z "$MODE" ]; then
  tgit write-tree > "$marker" || fail "could not record the staged tree"
  for need in "${RELEASE_NEEDS[@]}"; do
    [ -e "$TARGET/$need" ] || echo "    note: no $need yet; --commit needs the grouping UI the README describes"
  done
  step "staged only, nothing committed. Review the tree above, then re-run with --commit or --update \"<subject>\"."
  exit 0
fi

# --- commit ---------------------------------------------------------------------------------
expected_parent="$(tgit rev-parse -q --verify 'HEAD^{commit}' || true)"
# undo: drop the new commit (and tag), keep the staged tree (marked as this
# script's own staging), and leave no trace of the commit: reflog expired,
# unreachable objects pruned.
undo() {
  [ -z "${1:-}" ] || tgit tag -d "$1" > /dev/null 2>&1 || true
  if [ -n "$expected_parent" ]; then
    tgit update-ref HEAD "$expected_parent"
  else
    tgit update-ref -d HEAD
  fi
  tgit write-tree > "$marker" || true
  tgit reflog expire --expire=now --all || true
  tgit prune --expire=now || true
}
check_commit() {  # [tag]
  local parents who
  parents="$(tgit rev-list --parents -n 1 HEAD)"
  parents="${parents#* }"
  if [ -n "$expected_parent" ]; then
    [ "$parents" = "$expected_parent" ] || { undo "${1:-}"; fail "the new commit had unexpected parents: undone"; }
  else
    [ "$parents" = "$(tgit rev-parse HEAD)" ] || { undo "${1:-}"; fail "the first commit had parents: undone"; }
  fi
  who="$(tgit log -1 --format='%an <%ae>|%cn <%ce>|%G?')"
  [ "$who" = "$ident|$ident|N" ] || { undo "${1:-}"; fail "the new commit carried $who: undone"; }
  step "gate: check-private.sh --history with the deny-list"
  if ! (cd "$TARGET" && bash "$ROOT/scripts/check-private.sh" --history); then
    undo "${1:-}"
    fail "the new commit failed check-private --history (above): undone; the staged tree is kept"
  fi
  rm -f "$marker"
}

if [ "$MODE" = --update ]; then
  [ -n "$changes" ] || fail "nothing to commit"
  tgit commit -q --no-gpg-sign -F "$msg" || fail "commit failed"
  check_commit
  step "committed $(tgit rev-parse --short HEAD) (no tag)"
  mark_published
  push_cmd
  exit 0
fi

# A release with no tree change (the snapshot already went out as an update)
# still gets its own commit, so the tag names the release.
tgit commit -q --no-gpg-sign --allow-empty -F "$msg" || fail "commit failed"
tgit tag -a --no-sign "$tag" -m "sitboard $version" || { undo; fail "tag failed: commit undone"; }
[ "$(tgit for-each-ref "refs/tags/$tag" --format='%(taggername) %(taggeremail)')" = "$ident" ] \
  || { undo "$tag"; fail "tag $tag carried another identity: undone"; }
check_commit "$tag"
step "committed $(tgit rev-parse --short HEAD) and tagged $tag"
mark_published
push_cmd "$tag"
if [ "$REHEARSAL" != 1 ]; then
  echo "    then tag the private tree too: git tag -a $tag -m 'sitboard $version' && git push origin $tag"
  echo "    then build, verify and publish the archives: scripts/release.sh (see scripts/README.md)"
fi
