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
#     an editor swap or backup file, CLAUDE.md or AGENTS.md, or a path with a
#     control character (a newline, a tab); tracked files that .gitignore
#     ignores are refused too
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
#   PUBLISH_GATES_ONLY=1 scripts/publish.sh      (no target, no other switch)
#                             `make check`'s run: stage the tracked files in
#                             the scratch repository and run the fast gates
#                             (kinds, sizes, ignore rules, docs/ and website
#                             pointers, check-private from the staged copy),
#                             then stop. There is no target: nothing is probed
#                             or written, nothing is committed, no remote is
#                             read and git may not fetch (a missing object
#                             fails the gate). The clean-copy `make check` is
#                             skipped (it would run this again). The private
#                             tree may be dirty; only the staged snapshot
#                             decides (its .gitignore, its scanner, its
#                             Makefile's RELEASE_VERSION).
#                             PRIVATE_FLAGS may be empty or --no-denylist
#                             (CI), nothing else.
#   PUBLISH_TEST_INTERRUPT_AFTER=N  (with either) stop the write into the
#                             target after N files, as Ctrl-C would
# None of these reach the clean copy's own make check (which runs the gates
# only). Scratch files live in one folder under TMPDIR (made absolute), and
# every scanner and the clean copy get a TMPDIR inside it; the traps that
# remove it on exit, Ctrl-C or SIGTERM are set before it exists. An
# interrupted write puts the target back (removing only the paths it began,
# never through a link), or says it could not, keeps that folder, prunes
# nothing and writes recover.sh there to finish by hand.
# All git calls ignore the user's global and system git config and hooks.
set -euo pipefail

usage='usage: publish.sh <public-checkout> [--commit | --update "<subject>"]'
GATES_ONLY="${PUBLISH_GATES_ONLY:-0}"
case "$GATES_ONLY" in 0|1) ;; *) echo "publish: PUBLISH_GATES_ONLY must be 0 or 1" >&2; exit 2 ;; esac
if [ "$GATES_ONLY" = 1 ]; then
  [ $# -eq 0 ] || { echo "publish: PUBLISH_GATES_ONLY=1 takes no arguments (it has no target)" >&2; exit 2; }
  TARGET_ARG="" MODE=""
else
  TARGET_ARG="${1:?$usage}"
  MODE="${2:-}"
fi
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
[ "$GATES_ONLY" != 1 ] || [ -z "$TEST_REMOTE${PUBLISH_TEST_INTERRUPT_AFTER:-}" ] || fail "PUBLISH_GATES_ONLY=1 takes no rehearsal switch"
REHEARSAL=0
if [ "$TEST_TARGET" = 1 ] || [ -n "$TEST_REMOTE" ]; then REHEARSAL=1; fi

# TMPDIR, and the target, are resolved against the caller's folder, before
# anything moves: every temporary folder below, and every child's, is absolute.
TMPDIR="${TMPDIR:-/tmp}"
TMPDIR="$(cd "$TMPDIR" && pwd -P)" || fail "TMPDIR is not a folder"
export TMPDIR
if [ "$GATES_ONLY" != 1 ]; then
  [ -d "$TARGET_ARG" ] || fail "$TARGET_ARG is not a directory"
  TARGET="$(cd "$TARGET_ARG" && pwd -P)"
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$ROOT"

# No configuration may reach git through the environment.
unset GIT_CONFIG GIT_CONFIG_PARAMETERS GIT_CONFIG_COUNT GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE \
      GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_NAMESPACE GIT_COMMON_DIR
while IFS= read -r v; do unset "$v"; done < <(env | sed -n 's/^\(GIT_CONFIG_[A-Za-z0-9_]*\)=.*/\1/p')
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
# A gates-only run reads this repository and nothing else: git may not fetch a
# missing object on demand (a partial clone) or use any transport.
if [ "$GATES_ONLY" = 1 ]; then export GIT_NO_LAZY_FETCH=1 GIT_ALLOW_PROTOCOL=none GIT_TERMINAL_PROMPT=0; fi

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
  # The README's banner, by name too, and nothing else from that folder.
  .github/sitboard-banner.svg
  # Beyond the agreed list, needed so the public tree passes `make check`:
  testdata       # fixtures the adapter tests read
  test           # the end-to-end test package
  .golangci.yml  # the lint configuration `make lint` uses
  notices.go notices_test.go  # the root package that embeds LICENSE and the notices
)

# The kinds of file each top-level folder may export: "<folder>:<extensions>"
# and, for files without an extension or one file let out by name,
# "<folder>=<exact paths below that folder>". "." is the repository root,
# whose files are the allowlist's own names. Anything else stops the run and
# names the file.
KINDS=(
  ".:.go .md .mod .sum .yml"
  ".=Makefile LICENSE .gitignore"
  ".github:.yml"
  ".github=sitboard-banner.svg"
  "cmd:.go"
  "internal:.go .json .txt"
  # The list of facts README and the website are built from (public, so
  # readers can check the claims against the code); no other YAML.
  "internal=server/dist/.keep facts/facts.yaml"
  "scripts:.md .mjs .sh .tmpl"
  "test:.go"
  "testdata:.json .jsonl .md .txt"
  "web:.css .html .js .json .mjs .snap .svelte .svg .ts .txt .woff2"
)
# An exported .github SVG is plain drawing only (svg_plain below):
# - its tags are these elements and no other, spelled exactly (the banner
#   uses all of them and nothing else);
SVG_TAGS="svg g rect path text tspan"
# - its attributes are these names and no other, spelled exactly (again the
#   banner's own), each with a quoted value;
SVG_ATTRS="aria-label d fill fill-opacity font-family font-size font-weight height letter-spacing role rx stroke transform viewBox width x xmlns y"
# - and it holds none of these, ignoring case: "<!" (doctype, entity, comment,
#   CDATA), "<?", "&" (any entity or character reference), url( (paint or
#   anything else from elsewhere), an on...= event attribute, a prefixed
#   attribute such as xmlns:x= or xlink:href= (the plain xmlns="..." is the one
#   namespace declaration), and, kept from before the allowlist, script, href,
#   image, foreignObject, metadata, data:, Inkscape/Sodipodi residue and style.
SVG_REFUSED='<!|<\?|&|url\(|(^|[^a-z0-9:_.-])on[a-z]+[[:space:]]*=|[[:space:]][a-z_][a-z0-9._-]*:[a-z_][a-z0-9._-]*[[:space:]]*=|<script|href|<image|<foreignobject|<metadata|data:|sodipodi|inkscape|style'
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

# sq <string>: the string in single quotes, for a command printed for a human.
sq() { printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"; }
writing=0
keep_work=0
WORK=""
# safe_rm <top> <path>: removes <top>/<path> if it is a file or a link. A
# missing path, or a real folder (never one the write created), is left alone.
# Returns 1, removing nothing, when the path could lead outside <top>: a link
# on the way, or a ., .. or .git part.
safe_rm() {
  local top="$1" rel="$2" pre="" rest="$2"
  case "/$rel/" in *//*|*/./*|*/../*|*/.git/*) return 1 ;; esac
  while [ "${rest#*/}" != "$rest" ]; do
    pre="$pre${rest%%/*}"
    rest="${rest#*/}"
    if [ -L "$top/$pre" ]; then return 1; fi
    [ -d "$top/$pre" ] || return 0
    pre="$pre/"
  done
  if [ -d "$top/$rel" ] && [ ! -L "$top/$rel" ]; then return 0; fi
  rm -f -- "$top/$rel"
}
# restore_tree: the target's index and files as they were before the write.
restore_tree() { tgit read-tree "$pre_tree" && tgit checkout-index -a -f; }
# restore_target puts the target back as it was before the write step began,
# in two phases. Removal: each path the write had begun ($WORK/written, one per
# line: the export refuses control characters) is removed with safe_rm; when
# all are gone, $WORK/removed marks the phase done, and it is never replayed
# (a partial restore may have put an old link back). Restore: the old index
# and files. Anything that fails keeps the scratch folder, prunes nothing and
# writes recover.sh there, which redoes only the phases not yet done.
restore_target() {
  local f ok=1
  if [ ! -e "$WORK/removed" ]; then
    if [ -s "$WORK/written" ]; then
      while IFS= read -r f; do safe_rm "$TARGET" "$f" || ok=0; done < "$WORK/written" || ok=0
    fi
    if [ "$ok" = 1 ]; then : > "$WORK/removed" || ok=0; fi
  fi
  if [ "$ok" = 1 ] && restore_tree; then
    find "$TARGET" -mindepth 1 -type d -empty -not -path "$TARGET/.git" -not -path "$TARGET/.git/*" -delete 2>/dev/null || true
    tgit prune --expire=now 2>/dev/null || true   # the blobs the write had stored
    echo "publish: interrupted while writing; the target was put back as it was" >&2
    return 0
  fi
  keep_work=1
  write_recover > "$WORK/recover.sh" 2>/dev/null || true
  {
    echo "publish: interrupted while writing, and the target could NOT be put back: it may be partly written"
    echo "    the tree it had before is $pre_tree (not pruned); the files the write began are listed in $(sq "$WORK/written") (kept)"
    echo "    finish by hand: bash $(sq "$WORK/recover.sh")"
    echo "    then remove the scratch folder: rm -rf $(sq "$WORK")"
  } >&2
  return 1
}
# write_recover: recover.sh, for a human to finish a restore that failed. It
# runs with publish.sh's git environment (no GIT_DIR, GIT_WORK_TREE or config
# from the caller's shell) and target options, refuses unless the target is the
# same folder and repository (path and inode of both), and then runs the phases
# restore_target had not finished, with the same functions.
write_recover() {
  echo '#!/usr/bin/env bash'
  echo '# Written by publish.sh: finishes putting the target back after an interrupted write.'
  echo 'set -euo pipefail'
  echo 'unset GIT_CONFIG GIT_CONFIG_PARAMETERS GIT_CONFIG_COUNT GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE \'
  echo '      GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_NAMESPACE GIT_COMMON_DIR GIT_CEILING_DIRECTORIES GIT_DISCOVERY_ACROSS_FILESYSTEM'
  echo 'while IFS= read -r v; do unset "$v"; done < <(env | sed -n '"'"'s/^\(GIT_CONFIG_[A-Za-z0-9_]*\)=.*/\1/p'"'"')'
  echo 'export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1'
  echo "TARGET=$(sq "$TARGET")"
  echo "WORK=$(sq "$WORK")"
  echo "pre_tree=$pre_tree"
  echo "target_id=$(sq "$target_id")"
  declare -f tgit safe_rm restore_tree target_identity
  echo 'cd "$TARGET"'
  echo '[ "$(target_identity)" = "$target_id" ] || { echo "recover: $TARGET is not the checkout publish.sh wrote to (path, folder or repository changed): nothing done" >&2; exit 1; }'
  echo 'if [ ! -e "$WORK/removed" ]; then'
  echo '  while IFS= read -r f; do safe_rm "$TARGET" "$f" || { echo "recover: $f could lead outside the target (a link on the way): nothing more done" >&2; exit 1; }; done < "$WORK/written"'
  echo '  : > "$WORK/removed"'
  echo 'fi'
  echo 'restore_tree'
  echo 'find "$TARGET" -mindepth 1 -type d -empty -not -path "$TARGET/.git" -not -path "$TARGET/.git/*" -delete'
  echo 'tgit prune --expire=now'
  echo 'echo "recover: the target was put back; remove the scratch folder: rm -rf $WORK"'
}
# target_identity: the target's top level and git folder, by path and inode.
target_identity() {
  local top gd
  top="$(tgit rev-parse --show-toplevel)" && gd="$(tgit rev-parse --absolute-git-dir)" || return 1
  printf '%s %s %s %s' "$top" "$(ls -di "$top" | awk '{print $1}')" "$gd" "$(ls -di "$gd" | awk '{print $1}')"
}
pending=""
on_exit() {
  if [ "$writing" = 1 ]; then restore_target || true; fi
  [ "$keep_work" = 1 ] && return
  case "$WORK" in /?*) rm -rf "$WORK" ;; esac
}
# The scratch folder is made while a signal only sets $pending: whenever it
# arrives, WORK names the folder before the signal is acted on, so the cleanup
# always knows what to remove.
trap on_exit EXIT
trap 'pending=130' INT
trap 'pending=143' TERM
# An explicit template: macOS mktemp -d would ignore TMPDIR (made absolute above).
raw_work="$(mktemp -d "$TMPDIR/publish.XXXXXX")" || fail "could not create a scratch folder"
WORK="$raw_work"
trap 'exit 130' INT
trap 'exit 143' TERM
[ -z "$pending" ] || exit "$pending"
WORK="$(cd "$raw_work" && pwd -P)" || { WORK="$raw_work"; fail "could not enter the scratch folder"; }
case "$WORK" in /?*) ;; *) WORK="$raw_work"; fail "bad scratch folder: $WORK" ;; esac
# TMPDIR of every scanner and of the clean copy: the cleanup above covers it.
mkdir "$WORK/tmp" || fail "could not create a scratch folder"

# --- preflight: the source ---------------------------------------------------------
if [ "$TEST_TARGET" != 1 ] && [ "$GATES_ONLY" != 1 ]; then
  [ -z "$(pgit status --porcelain --untracked-files=no)" ] || fail "the private working tree is not clean"
fi
# (a gates-only run needs the staged scanner, checked after staging)
[ "$GATES_ONLY" = 1 ] || [ -f scripts/check-private.sh ] || fail "scripts/check-private.sh is missing"
# A gates-only run judges what is staged; a release, the commit it exports.
version_src=HEAD:Makefile
[ "$GATES_ONLY" != 1 ] || version_src=:Makefile
version="$(pgit show "$version_src" | sed -n 's/^RELEASE_VERSION := \([^ ]*\)$/\1/p')"
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
  # An entry below the top level names one file, and a path is also a folder
  # prefix to git: it must list exactly itself, never files under a folder of
  # that name.
  case "$p" in
    */*) [ "$(pgit ls-files -z -- "$p" | LC_ALL=C tr '\0' '\n')" = "$p" ] || fail "allowlisted file is not one tracked file: $p (a folder of that name?)" ;;
  esac
done

# svg_plain <file>: the SVG is plain drawing only (SVG_TAGS, SVG_ATTRS,
# SVG_REFUSED). XML lets a newline or a tab stand between any two parts of a
# tag, so every check reads a flattened copy, CR, LF and tab turned into
# spaces. It reads files, not pipes, so an early exit of grep cannot turn
# into a pass.
svg_plain() {
  local flat="$1.flat" all nonul
  # No NUL byte anywhere, past git's 8000-byte binary window too: some awks
  # (macOS) end a record at a NUL and would never see what follows it.
  all="$(wc -c < "$1")"; nonul="$(LC_ALL=C tr -d '\000' < "$1" | wc -c)"
  [ "$((all))" -eq "$((nonul))" ] || return 1
  LC_ALL=C tr '\r\n\t' '   ' < "$1" > "$flat" || return 1
  LC_ALL=C grep -aqiE -- "$SVG_REFUSED" "$flat" && return 1
  # Every "<" starts a tag or an end tag's name.
  LC_ALL=C grep -aqE -- '<([^A-Za-z/]|/[^A-Za-z]|/?$)' "$flat" && return 1
  # Walk the tags: each name is in SVG_TAGS, each attribute name in SVG_ATTRS
  # (exact spelling), each attribute is name="value" or name='value' with the
  # quote closed, and a tag ends with > or />. Quotes count inside tags only.
  LC_ALL=C awk -v tags="$SVG_TAGS" -v attrs="$SVG_ATTRS" -v sq="'" '
    BEGIN {
      nt = split(tags, t, " "); for (k = 1; k <= nt; k++) T[t[k]] = 1
      na = split(attrs, a, " "); for (k = 1; k <= na; k++) A[a[k]] = 1
    }
    {
      s = s $0
    }
    END {
      n = length(s); i = 1; tagsseen = 0
      while (i <= n) {
        if (substr(s, i, 1) != "<") { i++; continue }
        i++; endtag = 0
        if (substr(s, i, 1) == "/") { endtag = 1; i++ }
        j = i; while (i <= n && substr(s, i, 1) ~ /[A-Za-z0-9_.:-]/) i++
        name = substr(s, j, i - j)
        if (!(name in T)) exit 1
        tagsseen++
        while (1) {
          while (i <= n && substr(s, i, 1) == " ") i++
          if (i > n) exit 1
          c = substr(s, i, 1)
          if (c == ">") { i++; break }
          if (c == "/" && !endtag && substr(s, i + 1, 1) == ">") { i += 2; break }
          if (endtag) exit 1
          j = i; while (i <= n && substr(s, i, 1) ~ /[A-Za-z0-9_.:-]/) i++
          an = substr(s, j, i - j)
          if (an == "" || !(an in A)) exit 1
          while (i <= n && substr(s, i, 1) == " ") i++
          if (substr(s, i, 1) != "=") exit 1
          i++; while (i <= n && substr(s, i, 1) == " ") i++
          q = substr(s, i, 1)
          if (q != "\"" && q != sq) exit 1
          k = index(substr(s, i + 1), q)
          if (k == 0) exit 1
          i = i + 1 + k
        }
      }
      if (tagsseen == 0) exit 1
      exit 0
    }' "$flat" || return 1
  return 0
}

# kind_ok <path>: the file is of a kind listed for its top-level folder.
kind_ok() {
  local f="$1" top rest base ext k
  case "$f" in */*) top="${f%%/*}"; rest="${f#*/}" ;; *) top="."; rest="$f" ;; esac
  base="${f##*/}"
  ext=""
  case "$base" in ?*.*) ext=".${base##*.}" ;; esac
  for k in "${KINDS[@]}"; do
    case "$k" in
      "$top:"*) [ -n "$ext" ] && [[ " ${k#*:} " == *" $ext "* ]] && return 0 ;;
      "$top="*) [[ " ${k#*=} " == *" $rest "* ]] && return 0 ;;
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
  # No control character (a newline, a tab, an escape) in an exported path:
  # every list of paths below is one per line, and a terminal shows them as is.
  if [ "$(printf '%s' "$f" | LC_ALL=C tr -d '\000-\037\177')" != "$f" ]; then
    bad="$bad$(printf '%q' "$f"): a control character in the path (a newline, a tab, ...)"$'\n'; continue
  fi
  # Bytes, not characters (LC_ALL=C): a path that is not UTF-8 is lowercased
  # whole, so the checks below see all of it.
  lc="$(printf '%s' "$f" | LC_ALL=C tr '[:upper:]' '[:lower:]')"
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
  # A picture in .github is shown on the repository page: plain drawing only.
  case "$lc" in
    .github/*.svg)
      pgit cat-file blob "$blob" > "$WORK/svg" || fail "could not read $f"
      svg_plain "$WORK/svg" \
        || bad="$bad$f: an SVG that is not plain drawing (SVG_TAGS, SVG_ATTRS and SVG_REFUSED in scripts/publish.sh)"$'\n' ;;
  esac
  # git's binary test: a NUL byte in the first 8000 bytes. Counted in bytes
  # (LC_ALL=C): under a UTF-8 locale macOS tr stops at the first byte that is
  # not UTF-8, which would call such text binary.
  all="$(pgit cat-file blob "$blob" | head -c 8000 | wc -c || true)"
  nonul="$(pgit cat-file blob "$blob" | head -c 8000 | LC_ALL=C tr -d '\000' | wc -c || true)"
  if [ "$all" != "$nonul" ] && [[ " ${BINARY_ALLOWED[*]} " != *" $f "* ]]; then
    bad="$bad$f: binary content (BINARY_ALLOWED in scripts/publish.sh)"$'\n'
  fi
  size="$(pgit cat-file -s "$blob")" || fail "could not read the size of $f (a missing object fails the gate)"
  if [ "$size" -gt "$MAX_BYTES" ] && [[ " ${SIZE_EXCEPTIONS[*]-} " != *" $f "* ]]; then
    bad="$bad$f: $size bytes, more than $MAX_BYTES (SIZE_EXCEPTIONS in scripts/publish.sh)"$'\n'
  fi
done < "$WORK/export"
[ -z "$bad" ] || { printf '%s' "$bad" >&2; fail "forbidden files in the export set (above)"; }
# (a gates-only run judges the staged .gitignore, below, not the worktree's)
if [ "$GATES_ONLY" != 1 ]; then
  ignored="$(pgit ls-files -ci --exclude-standard -- "${ALLOWLIST[@]}")"
  [ -z "$ignored" ] || { echo "$ignored" >&2; fail "tracked files that .gitignore ignores are in the export set (above)"; }
fi

if [ "$MODE" = --commit ]; then
  missing=""
  for need in "${RELEASE_NEEDS[@]}"; do
    pgit ls-files --error-unmatch -- "$need" > /dev/null 2>&1 || missing="$missing $need"
  done
  [ -z "$missing" ] || fail "the README and CHANGELOG describe project tiles and the side rail, but the export lacks:$missing (merge the grouping UI first)"
fi

# --- preflight: the target (a gates-only run has none) --------------------------------
if [ "$GATES_ONLY" != 1 ]; then
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
  done < <(ref_names | LC_ALL=C sort -u)
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
    # the target write records each path before it begins (the rollback's list)
    if [ "$g" = tgit ]; then printf '%s\n' "$f" >> "$WORK/written"; fi
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
  "$g" ls-files -z | LC_ALL=C tr '\0' '\n' | LC_ALL=C sort > "$WORK/staged.sorted"
  cmp -s "$WORK/exported.sorted" "$WORK/staged.sorted" \
    || { diff "$WORK/exported.sorted" "$WORK/staged.sorted" >&2 || true; fail "the staged files are not exactly the exported files"; }
}
write_tree "$STAGE" sgit

# The ignore rules the public tree carries apply to the public tree: tracked
# files its .gitignore ignores are refused. A real export also checked this
# worktree's rules above; a gates-only run judges these alone (its worktree
# may differ from what is staged), and needs the staged scanner.
[ -f "$STAGE/scripts/check-private.sh" ] || fail "the staged snapshot has no scripts/check-private.sh"
ignored="$(sgit ls-files -ci --exclude-standard)" || fail "could not check the staged files against .gitignore"
[ -z "$ignored" ] || { echo "$ignored" >&2; fail "staged files that the exported .gitignore ignores (above)"; }

# No pointer to a docs/ file the snapshot does not carry: a path that starts
# docs/ (after any ./ or ../) at a word boundary; inside a URL it is not one.
# scan <outfile> <pattern>: the matches (file:line:text) of a search over the
# staged tree. grep's "no match" (1) is the clean answer; any other status is a
# search that could not finish, and a search that did not finish is not clean.
scan() {
  local out="$1" rc=0
  # a failed cd has its own status (99), never grep's "no match" (1)
  (cd "$STAGE" || exit 99; LC_ALL=C grep -rIonE --exclude-dir=.git "$2" .) > "$out.raw" || rc=$?
  [ "$rc" -le 1 ] || fail "searching the staged tree failed (grep status $rc): the gate cannot say it is clean"
  sed 's|^\./||' "$out.raw" > "$out" || fail "could not read the search result"
}
dead=""
scan "$WORK/docs-hits" '(^|[^A-Za-z0-9._/-])(\./|\.\./)*docs/[A-Za-z0-9._/-]*[A-Za-z0-9_-]'
while IFS= read -r hit; do
  ref="${hit#*:*:}"
  ref="docs/${ref#*docs/}"
  [ -e "$STAGE/$ref" ] || dead="$dead${hit%%:*}:$(cut -d: -f2 <<<"$hit"): $ref is not exported"$'\n'
done < "$WORK/docs-hits"
[ -z "$dead" ] || { printf '%s' "$dead" >&2; fail "the public tree points at docs/ files it does not carry: say the fact where it is used"; }
# No reference to the website's folder (with or without a leading / or any
# ./ or ../; inside a URL such as https://host/<folder>/ it is not one).
scan "$WORK/site-hits" "(^|[^A-Za-z0-9._/-])(\\./|\\.\\./|/)*$SITE_DIR/"
[ ! -s "$WORK/site-hits" ] || { cat "$WORK/site-hits" >&2; fail "the public tree refers to the website's folder (above): the website is not published here"; }

step "gate: scripts/check-private.sh with the deny-list"
# A gates-only run (no real export) runs the staged copy of the scanner, so a
# worktree edit cannot stand in for what would be exported, and takes one flag
# at most: --no-denylist (CI has no deny-list). Any other value, --help
# included, would let the scanner exit 0 without scanning. A real export
# always runs with the deny-list, whatever the environment says.
cp_script="$ROOT/scripts/check-private.sh"
cp_flags=()
if [ "$GATES_ONLY" = 1 ]; then
  cp_script="$STAGE/scripts/check-private.sh"
  case "${PRIVATE_FLAGS:-}" in
    "") ;;
    --no-denylist) cp_flags=(--no-denylist) ;;
    *) fail "PRIVATE_FLAGS may be empty or --no-denylist in a gates-only run" ;;
  esac
fi
(cd "$STAGE" && env TMPDIR="$WORK/tmp" bash "$cp_script" ${cp_flags[@]+"${cp_flags[@]}"}) \
  || fail "the public tree failed check-private (above); fix it in the private repository"

if [ "$GATES_ONLY" = 1 ]; then
  step "gates passed (stage only: the clean copy was not built, nothing was written)"
  exit 0
fi

step "gate: a clean copy passes make check and make build"
build="$WORK/build"
mkdir "$build"
sgit checkout-index -a --prefix="$build/" || fail "could not copy the staged tree"
git -C "$build" init -q -b main
git -C "$build" add -A
git -C "$build" -c core.hooksPath=/dev/null -c user.name=publish-gate -c user.email=gate@example.com \
  -c commit.gpgsign=false commit -q -m "snapshot" || fail "could not commit the clean copy"
# A make that started this script must not steer the clean copy's: its flags
# and its level would come along (GNU make then also prints "Entering
# directory" lines, which would push the result out of the summary below).
if ! (cd "$build" && unset MAKEFLAGS MFLAGS GNUMAKEFLAGS MAKEFILES MAKELEVEL \
  PUBLISH_TEST_TARGET PUBLISH_TEST_REMOTE PUBLISH_TEST_INTERRUPT_AFTER PUBLISH_GATES_ONLY PUBLISH_IDENT \
  && export TMPDIR="$WORK/tmp" \
  && env -u CI make check PRIVATE_FLAGS=--no-denylist && make build) > "$WORK/build.log" 2>&1; then
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
  env TMPDIR="$WORK/tmp" bash "$ROOT/scripts/check-private.sh" --text "$msg" || fail "the commit message failed check-private (above)"
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
target_id="$(target_identity)" || fail "could not read the target's identity"
: > "$WORK/written"
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
  if ! (cd "$TARGET" && env TMPDIR="$WORK/tmp" bash "$ROOT/scripts/check-private.sh" --history); then
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
