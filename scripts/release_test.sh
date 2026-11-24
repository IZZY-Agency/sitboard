#!/usr/bin/env bash
# Tests for the release scripts: publish.sh, package.sh, verify-release.sh,
# render-formula.sh and tap.sh. Each case builds throwaway private, public and
# tap repositories from a small stand-in for sitboard (same module path, same
# version flag), so the real repository, any real public checkout and the real
# tap are never touched, and nothing goes over the network: git may only use
# local transports, and the scripts read assets and history from local
# folders through their rehearsal switches.
#
#   bash scripts/release_test.sh
# shellcheck disable=SC2015,SC2016,SC2012 # ok() always returns 0; backticks are Markdown; ls lists known names
set -uo pipefail

# Hermetic git: no user or system config, no prompts, local transports only.
unset GIT_CONFIG GIT_CONFIG_PARAMETERS GIT_CONFIG_COUNT GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
while IFS= read -r v; do unset "$v"; done < <(env | sed -n 's/^\(GIT_CONFIG_[A-Za-z0-9_]*\)=.*/\1/p')
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_TERMINAL_PROMPT=0
export GIT_ASKPASS=/usr/bin/false SSH_ASKPASS=/usr/bin/false GIT_ALLOW_PROTOCOL=file
export HOMEBREW_NO_AUTO_UPDATE=1
unset PUBLISH_TEST_TARGET PUBLISH_TEST_REMOTE PUBLISH_IDENT TAP_TEST_ASSETS_DIR TAP_TEST_REMOTE
# CI and the real export run `make check PRIVATE_FLAGS=--no-denylist`, which
# reaches this script's environment: the cases set the flag themselves.
unset PRIVATE_FLAGS PUBLISH_GATES_ONLY PUBLISH_TEST_INTERRUPT_AFTER
unset SIGN_IDENTITY NOTARY_PROFILE COMMIT PACKAGE_ALLOW_DIRTY PACKAGE_DEV REQUIRE_SIGNATURE EXPECTED_TEAM_ID

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repo="$(cd "$here/.." && pwd -P)"
work="$(mktemp -d)" || { echo "release_test: mktemp failed" >&2; exit 2; }
work="$(cd "$work" && pwd -P)"
case "$work" in /?*) ;; *) echo "release_test: bad scratch folder" >&2; exit 2 ;; esac
trap 'rm -rf "$work"' EXIT

PUBLIC_URL="https://github.com/IZZY-Agency/sitboard.git"
TAP_URL="https://github.com/IZZY-Agency/homebrew-tap.git"
IDENT="Test Publisher <publisher@example.com>"
V="9.9.9"
dd="docs"   # the unexported folder, never spelled as a path in this exported file
ss="site"   # the website's folder: never exported, never spelled as a path here
deny_word="zorblaxian"

# No script prints an address with the host twice (issue 45): expect checks
# every output it captures, and host_twice the outputs captured without it.
DOUBLED_HOST="github.com/github.com"
failures=0
ok()  { echo "ok   $1"; }
bad() { echo "FAIL $1"; printf '%s\n' "$2" | sed 's/^/    /'; failures=$((failures + 1)); }
# host_twice <name> <output>: fails <name> when <output> has the host twice.
host_twice() {
  [[ "$2" == *"$DOUBLED_HOST"* ]] || return 1
  bad "$1" "an address has the host twice ($DOUBLED_HOST):
$2"
}
# expect <name> <exit> <needle or -> <command...>: the command exits <exit>
# and, unless the needle is "-", prints the needle.
expect() {
  local name="$1" want="$2" needle="$3" out rc
  shift 3
  out="$("$@" 2>&1)"; rc=$?
  if host_twice "$name" "$out"; then
    :
  elif [[ "$rc" == "$want" && ( "$needle" == "-" || "$out" == *"$needle"* ) ]]; then
    ok "$name"
  else
    bad "$name" "exit $rc, want $want; want output: $needle
$out"
  fi
  LAST_OUT="$out"
}
commit_all() {
  git -C "$1" add -A
  git -C "$1" -c user.name=test -c user.email=test@example.com -c commit.gpgsign=false \
    commit -q --allow-empty -m "${2:-change}"
}

# A deny-list for the gate, outside every repository.
cfg="$work/cfg"
mkdir -p "$cfg/sitboard-dev"
printf '%s\n' "$deny_word" > "$cfg/sitboard-dev/denylist"
export XDG_CONFIG_HOME="$cfg"

# make_private <name> [<path>]: a small private repository laid out like
# sitboard: the exported paths, the release scripts, and private material
# beside them.
make_private() {
  local p="${2:-$work/private-$1}"
  mkdir -p "$p"/{cmd/sitboard,internal/buildinfo,internal/server/dist,web/src,scripts/formula} \
           "$p"/{.github/workflows,testdata/fixtures,test/e2e} "$p/$dd/superpowers" "$p/$dd/spikes"
  cp "$here"/{publish.sh,package.sh,notarize.sh,verify-release.sh,tap.sh,render-formula.sh,release.sh,check-private.sh} "$p/scripts/"
  cp "$here/formula/sitboard.rb.tmpl" "$p/scripts/formula/"
  cp "$repo/.gitignore" "$p/.gitignore"
  printf 'module github.com/IZZY-Agency/sitboard\n\ngo 1.27\n' > "$p/go.mod"
  : > "$p/go.sum"
  cat > "$p/internal/buildinfo/buildinfo.go" <<'EOF'
package buildinfo

var (
	Version = "dev"
	Commit  = "unknown"
)
EOF
  cat > "$p/cmd/sitboard/main.go" <<'EOF'
package main

import (
	"fmt"
	"os"

	"github.com/IZZY-Agency/sitboard/internal/buildinfo"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Printf("sitboard %s (%s)\n", buildinfo.Version, buildinfo.Commit)
		return
	}
	fmt.Println("board")
}
EOF
  : > "$p/internal/server/dist/.keep"
  echo '<!doctype html><title>sitboard</title>' > "$p/internal/server/dist/index.html"  # the web build: ignored
  echo 'export const x = 1;' > "$p/web/src/a.ts"
  cat > "$p/Makefile" <<EOF
RELEASE_VERSION := $V

check:
	@test ! -f break-check || { echo "check broke"; exit 1; }
	@echo "check passed with \$(PRIVATE_FLAGS)"

build:
	@echo built
EOF
  echo 'name: ci' > "$p/.github/workflows/ci.yml"
  echo 'version: "2"' > "$p/.golangci.yml"
  echo '{"name":"placeholder"}' > "$p/testdata/fixtures/f.json"
  echo 'package e2e' > "$p/test/e2e/e2e_test.go"
  echo 'MIT License' > "$p/LICENSE"
  echo '# Third-party notices' > "$p/THIRD_PARTY_NOTICES.md"
  printf 'package sitboard\n' > "$p/notices.go"
  printf 'package sitboard\n' > "$p/notices_test.go"
  printf '# sitboard\n\nSee https://example.com/docs/intro for more.\n' > "$p/README.md"
  read -r -a needs <<<"$(sed -n 's/^RELEASE_NEEDS=(\(.*\))$/\1/p' "$here/publish.sh")"
  for need in "${needs[@]}"; do
    mkdir -p "$(dirname "$p/$need")"; echo '// grouping UI' > "$p/$need"
  done
  printf '# Changelog\n\n## %s\n\nFirst release.\n\n- One.\n\n## 9.9.8\n\nOlder.\n' "$V" > "$p/CHANGELOG.md"
  echo "plan about $deny_word" > "$p/$dd/superpowers/plan.md"
  echo 'a spike note' > "$p/$dd/spikes/1.md"
  mkdir -p "$p/$ss"; echo '<!doctype html><title>sitboard.com</title>' > "$p/$ss/index.html"
  printf 'name: website\njobs:\n  deploy:\n    runs-on: ubuntu-latest\n' > "$p/.github/workflows/site.yml"
  # The README's banner is exported by name; a second picture beside it is not.
  echo '<svg xmlns="http://www.w3.org/2000/svg"/>' > "$p/.github/sitboard-banner.svg"
  echo '<svg xmlns="http://www.w3.org/2000/svg"/>' > "$p/.github/draft-banner.svg"
  git -C "$p" init -q -b main
  commit_all "$p" init
  echo "$p"
}
make_target() { local t="$work/public-$1"; git init -q -b main "$t"; echo "$t"; }
# A public checkout for a commit rehearsal: its origin is a local bare
# repository standing in for the public one ($work/bare-<name>.git).
make_remote_target() {
  local b="$work/bare-$1.git" t="$work/public-$1"
  git init -q --bare -b main "$b"
  git init -q -b main "$t"
  git -C "$t" remote add origin "$b"
  echo "$t"
}
rehearse() { env PUBLISH_TEST_TARGET=1 bash "$@"; }
# state <checkout>: everything a run could change: HEAD, refs, index, the
# working tree's files and the object count.
state() {
  { git -C "$1" rev-parse -q --verify HEAD || echo unborn
    git -C "$1" for-each-ref
    git -C "$1" ls-files -s
    (cd "$1" && find . -path ./.git -prune -o -type f -print | LC_ALL=C sort | xargs cat 2>/dev/null | cksum)
    git -C "$1" count-objects -v | grep -E '^(count|in-pack):'
  } 2>/dev/null
}
# a target with some published content already in it
seeded_target() {
  local t; t="$(make_target "$1")"
  echo 'published before' > "$t/OLD.md"; commit_all "$t" "published before"
  echo "$t"
}
upper() { printf '%s' "$1" | tr '[:lower:]' '[:upper:]'; }
# case_insensitive <path>: the file system answers to another spelling.
case_insensitive() { [ "$(upper "$1")" != "$1" ] && [ -e "$(upper "$1")" ] && [ "$(upper "$1")" -ef "$1" ]; }

# --- publish.sh ------------------------------------------------------------------

# P1. A rehearsal stages exactly the allowlisted tracked files, keeps modes,
# runs every gate and commits nothing; a second run accepts its own staging.
p="$(make_private p1)"; t="$(make_target p1)"
echo 'loose' > "$p/notes.txt"
expect "rehearsal stages the export and commits nothing" 0 "staged only" rehearse "$p/scripts/publish.sh" "$t"
got="$(git -C "$t" ls-files | LC_ALL=C sort | tr '\n' ' ')"
want="$(cd "$p" && git ls-files -- cmd internal web scripts Makefile go.mod go.sum README.md LICENSE THIRD_PARTY_NOTICES.md CHANGELOG.md \
  .github/workflows/ci.yml .github/sitboard-banner.svg .gitignore testdata test .golangci.yml notices.go notices_test.go | LC_ALL=C sort | tr '\n' ' ')"
if [[ "$got" == "$want" && "$got" == *".github/sitboard-banner.svg"* && "$got" != *"$dd/"* && "$got" != *notes.txt* && "$got" != *"$ss/"* \
      && "$got" != *site.yml* && "$got" != *draft-banner.svg* ]]; then
  ok "the export is the allowlist with the README banner, without docs, the website, its workflow, another .github file or untracked files"
else
  bad "the export is the allowlist, without docs or untracked files" "got:  $got
want: $want"
fi
[[ "$(git -C "$t" ls-files -s scripts/publish.sh | cut -d' ' -f1)" == 100755 ]] \
  && ok "file modes are kept" || bad "file modes are kept" "$(git -C "$t" ls-files -s scripts/publish.sh)"
git -C "$t" rev-parse -q --verify HEAD >/dev/null && bad "rehearsal committed" "" || ok "rehearsal made no commit"
[[ "$LAST_OUT" == *"check passed with --no-denylist"* ]] \
  && ok "the build gate ran make check with --no-denylist" || bad "build gate output" "$LAST_OUT"
[[ "$LAST_OUT" == *"check-private: clean (tree mode; deny-list used)"* ]] \
  && ok "the deny-list gate ran on the export" || bad "deny-list gate output" "$LAST_OUT"
expect "a second stage-only run accepts the staging it left" 0 "staged only" rehearse "$p/scripts/publish.sh" "$t"

# P2. The deny-list gate: a private word in an exported file stops the export;
# without a deny-list the export fails closed.
p="$(make_private p2)"; t="$(make_target p2)"
echo "// about $deny_word" >> "$p/cmd/sitboard/main.go"; commit_all "$p"
expect "a deny-list word in the export stops it" 1 "the public tree failed check-private" \
  rehearse "$p/scripts/publish.sh" "$t"
[[ "$LAST_OUT" != *orblaxian* ]] && ok "the gate never prints the word" || bad "the gate printed the word" "$LAST_OUT"
p="$(make_private p2b)"; t="$(make_target p2b)"
expect "no deny-list: the export fails closed" 1 "deny-list missing, run scripts/make-denylist.sh" \
  env XDG_CONFIG_HOME="$work/no-cfg" PUBLISH_TEST_TARGET=1 bash "$p/scripts/publish.sh" "$t"
expect "PRIVATE_FLAGS=--no-denylist cannot weaken a real export: still fails closed" 1 "deny-list missing, run scripts/make-denylist.sh" \
  env XDG_CONFIG_HOME="$work/no-cfg" PRIVATE_FLAGS=--no-denylist PUBLISH_TEST_TARGET=1 bash "$p/scripts/publish.sh" "$t"
gates() { env PUBLISH_GATES_ONLY=1 PRIVATE_FLAGS= "$@"; }   # gates() [VAR=value ...] bash <publish.sh>: no target, the deny-list on unless a case says otherwise
expect "gates-only with PRIVATE_FLAGS=--no-denylist runs without a deny-list" 0 "gates passed" \
  gates XDG_CONFIG_HOME="$work/no-cfg" PRIVATE_FLAGS=--no-denylist bash "$p/scripts/publish.sh"
expect "gates-only without a deny-list and without the flag fails closed" 1 "deny-list missing" \
  gates XDG_CONFIG_HOME="$work/no-cfg" bash "$p/scripts/publish.sh"
for fl in --help --history "--no-denylist --help" "--text /dev/null"; do
  expect "gates-only refuses PRIVATE_FLAGS='$fl' (the scanner could exit 0 unscanned)" 1 "PRIVATE_FLAGS may be empty or --no-denylist" \
    gates PRIVATE_FLAGS="$fl" bash "$p/scripts/publish.sh"
done

# P3. The build gate: the export must pass make check from a clean clone.
# Here the Makefile needs a file the allowlist leaves out.
p="$(make_private p3)"; t="$(make_target p3)"
echo 'kept private' > "$p/extra.txt"
sed -i.bak "s/test ! -f break-check/test -f extra.txt/" "$p/Makefile"; rm "$p/Makefile.bak"; commit_all "$p"
expect "an export missing a file make check needs is refused" 1 "the exported tree does not pass" \
  rehearse "$p/scripts/publish.sh" "$t"
p="$(make_private p3b)"; t="$(make_target p3b)"
sed -i.bak 's/test ! -f break-check/test ! -f cmd\/sitboard\/main.go/' "$p/Makefile"; rm "$p/Makefile.bak"; commit_all "$p"
expect "the build gate runs the exported Makefile" 1 "the exported tree does not pass" \
  rehearse "$p/scripts/publish.sh" "$t"

# P3c. The export may not point at docs/ files it does not carry; a URL
# that merely has /docs/ in it is fine (P1's README has one).
p="$(make_private p3c)"; t="$(make_target p3c)"
printf '// The rule is explained in %s/spikes/1.md.\n' "$dd" >> "$p/cmd/sitboard/main.go"; commit_all "$p"
expect "a reference to an unexported docs/ file is refused" 1 "cmd/sitboard/main.go:" \
  rehearse "$p/scripts/publish.sh" "$t"
[[ "$LAST_OUT" == *"not exported"* ]] && ok "the docs/ gate names the reference" || bad "docs gate message" "$LAST_OUT"

# P3d (website). A path to the website's folder in the export set stops the
# run, and so does any reference to that folder in the exported files; a link
# to the website itself is fine (P1's tree has none to trip over).
p="$(make_private p3d)"; t="$(make_target p3d)"
sed -i.bak "s|^  testdata  |  $ss testdata  |" "$p/scripts/publish.sh"; rm "$p/scripts/publish.sh.bak"; commit_all "$p"
expect "a website path in the export set is refused" 1 "the website" rehearse "$p/scripts/publish.sh" "$t"
[[ -z "$(git -C "$t" ls-files)" ]] && ok "the refused website export touched nothing" || bad "website export touched the target" ""
p="$(make_private p3e)"; t="$(make_target p3e)"
printf '// The page is built from %s/index.html.\n' "$ss" >> "$p/cmd/sitboard/main.go"
printf 'Website: https://sitboard.com\n' >> "$p/README.md"; commit_all "$p"
expect "a reference to the website's folder is refused" 1 "cmd/sitboard/main.go:" rehearse "$p/scripts/publish.sh" "$t"
[[ "$LAST_OUT" != *"README.md:"* ]] && ok "a link to https://sitboard.com is fine" || bad "the website link was flagged" "$LAST_OUT"

# P4. Symlinks are never exported.
p="$(make_private p4)"; t="$(seeded_target p4)"
ln -s ../README.md "$p/scripts/link.md"; commit_all "$p"
before="$(state "$t")"
expect "a symlink in the export set is refused" 1 "not a regular file" rehearse "$p/scripts/publish.sh" "$t"
[[ "$(state "$t")" == "$before" ]] && ok "a refused export leaves a published target untouched" || bad "target touched" "$(git -C "$t" status --short)"

# P5 (review 2). Private kinds of files inside allowlisted folders, and
# tracked files .gitignore ignores, are refused before anything is touched.
n=0
dd_up="$(printf '%s' "$dd" | sed 's/^d/D/')"
for f in cmd/.env cmd/.env.local "internal/$dd/plan.md" internal/x/CLAUDE.md cmd/AGENTS.md \
         internal/.codex/s.json internal/.claude/s.json web/key.pem scripts/id.key \
         web/.npmrc internal/x/id_rsa cmd/.netrc cmd/cert.p12 internal/state.sqlite cmd/.main.go.swp \
         cmd/.ENV web/KEY.PEM "internal/$dd_up/plan.md" cmd/claude.md internal/agents.md \
         web/src/data.bin internal/x/notes.yaml; do
  n=$((n + 1))
  p="$(make_private "p5-$n")"; t="$(seeded_target "p5-$n")"
  mkdir -p "$(dirname "$p/$f")"; echo 'placeholder' > "$p/$f"
  git -C "$p" add -f -- "$f"; commit_all "$p"
  before="$(state "$t")"
  expect "export refuses $f" 1 "forbidden files in the export set" rehearse "$p/scripts/publish.sh" "$t"
  [[ "$(state "$t")" == "$before" ]] || bad "export of $f touched the target" ""
done
# Secrets inside files of allowed kinds: the guard's generic rules, run in
# the scratch repository before the target is written to.
k_aws="AK""IA$(printf 'Q%.0s' $(seq 1 16))"
k_slack="xox""b-1234567890-abcdefghij"
k_key="-----BEGIN ""OPENSSH PRIV""ATE KEY-----"
n=0
for planted in "internal/x/creds.json:{\"key\":\"$k_aws\"}" "web/src/slack.ts:export const t = '$k_slack';" \
               "scripts/k.md:$k_key"; do
  n=$((n + 1))
  f="${planted%%:*}"
  p="$(make_private "p5s-$n")"; t="$(seeded_target "p5s-$n")"
  mkdir -p "$(dirname "$p/$f")"; printf '%s\n' "${planted#*:}" > "$p/$f"; commit_all "$p"
  before="$(state "$t")"
  expect "export refuses a secret in $f" 1 "the public tree failed check-private" rehearse "$p/scripts/publish.sh" "$t"
  [[ "$(state "$t")" == "$before" ]] && ok "the refused secret left nothing in the target" || bad "secret export touched the target" "$(git -C "$t" count-objects -v)"
done
# Review 3.5: binary content stops the run whatever the name; names that
# merely contain a key marker do too.
for planted in internal/x/data.go testdata/fixtures/x.jsonl; do
  p="$(make_private "p5b-${planted##*/}")"; t="$(make_target "p5b-${planted##*/}")"
  mkdir -p "$(dirname "$p/$planted")"; printf 'package x\n\000\001\002' > "$p/$planted"; commit_all "$p"
  expect "binary content in $planted is refused" 1 "binary content" rehearse "$p/scripts/publish.sh" "$t"
done
for f in cmd/server.pem.go web/src/id_rsa_backup.ts cmd/x.key.go; do
  p="$(make_private "p5n-${f##*/}")"; t="$(make_target "p5n-${f##*/}")"
  mkdir -p "$(dirname "$p/$f")"; echo 'package x' > "$p/$f"; commit_all "$p"
  expect "a name containing a key marker is refused: $f" 1 "a private kind of file" rehearse "$p/scripts/publish.sh" "$t"
done
# The binary test is git's: a NUL byte in the first 8000 bytes, counted byte
# by byte whatever the locale. Under a UTF-8 locale macOS tr stops at the first
# byte that is not UTF-8 and says "Illegal byte sequence".
# The four fonts export without a word from tr.
p="$(make_private p5-fonts)"; t="$(make_target p5-fonts)"
mkdir -p "$p/web/src/fonts"; cp "$repo"/web/src/fonts/*.woff2 "$p/web/src/fonts/"; commit_all "$p"
expect "the four fonts export as allowed binary files" 0 "staged only" env LC_ALL=en_US.UTF-8 PUBLISH_TEST_TARGET=1 bash "$p/scripts/publish.sh" "$t"
[[ "$LAST_OUT" != *"Illegal byte sequence"* ]] && ok "the binary test reads the fonts without an illegal byte sequence" \
  || bad "the binary test stumbled on the fonts" "$(grep -m3 'Illegal byte sequence' <<<"$LAST_OUT")"
[[ "$(git -C "$t" ls-files web/src/fonts | wc -l | tr -d ' ')" == 4 ]] && ok "the four fonts are in the export" \
  || bad "fonts missing from the export" "$(git -C "$t" ls-files web/src/fonts)"
# Text that is not UTF-8 but has no NUL byte is text, on macOS as on Linux.
p="$(make_private p5-latin1)"; t="$(make_target p5-latin1)"
printf 'caf\351 au lait\n' > "$p/web/src/latin1.txt"; commit_all "$p"
expect "a Latin-1 text file without a NUL byte is not binary" 0 "staged only" env LC_ALL=en_US.UTF-8 PUBLISH_TEST_TARGET=1 bash "$p/scripts/publish.sh" "$t"
git -C "$t" ls-files --error-unmatch web/src/latin1.txt >/dev/null 2>&1 && ok "the Latin-1 file is in the export" \
  || bad "the Latin-1 file is missing from the export" "$(git -C "$t" ls-files web/src)"
# A path that is not UTF-8 is still checked whole: a docs folder below it is
# refused. The path lives in the index only (macOS file systems refuse it).
p="$(make_private p5-path)"; t="$(make_target p5-path)"
blob="$(echo 'export const y = 2;' | git -C "$p" hash-object -w --stdin)"
git -C "$p" update-index --add --cacheinfo "100644,$blob,web/x"$'\377'"/$dd/a.ts"
git -C "$p" -c user.name=test -c user.email=test@example.com -c commit.gpgsign=false commit -q -m 'a path that is not UTF-8'
expect "a docs folder below a path that is not UTF-8 is refused" 1 "inside a .claude, .codex or $dd folder" env LC_ALL=en_US.UTF-8 PUBLISH_TEST_TARGET=1 bash "$p/scripts/publish.sh" "$t"
p="$(make_private p5-big)"; t="$(make_target p5-big)"
head -c 1100000 /dev/zero | tr '\0' 'a' > "$p/web/src/big.json"; commit_all "$p"
expect "export refuses a file over the size cap" 1 "more than 1048576" rehearse "$p/scripts/publish.sh" "$t"
# PUBLISH_GATES_ONLY=1 (make check's export-gates): no target; the gates run on
# the staged files and stop. The exported Makefile of this stand-in cannot pass
# make check, so a full export fails at the clean copy while gates-only passes,
# and gates-only still stops before the build if its early exit ever moves.
p="$(make_private p5-go)"; t="$(make_target p5-go)"
printf 'RELEASE_VERSION := %s\n\ncheck:\n\t@echo broken; exit 1\n\nbuild:\n\t@echo built\n' "$V" > "$p/Makefile"
commit_all "$p"
expect "a full export of the stand-in fails at the clean copy's make check" 1 "does not pass make check" rehearse "$p/scripts/publish.sh" "$t"
before="$(state "$t")"
expect "gates-only passes the same tree without building the clean copy" 0 "gates passed" gates bash "$p/scripts/publish.sh"
[ "$(state "$t")" = "$before" ] && ok "gates-only does not touch a checkout beside it" || bad "gates-only changed the checkout" "$(state "$t")"
expect "gates-only takes no target" 2 "takes no arguments" gates bash "$p/scripts/publish.sh" "$t"
expect "gates-only takes no rehearsal switch" 1 "no rehearsal switch" gates PUBLISH_TEST_REMOTE="$t" bash "$p/scripts/publish.sh"
[ -z "$(git -C "$t" ls-files)" ] && ok "the checkout beside it is still empty" || bad "files in the checkout" "$(git -C "$t" ls-files)"
# git may not fetch: the environment git runs in says so (a git that reports it).
shim="$work/shim"; mkdir -p "$shim"
real_git="$(command -v git)"
printf '#!/bin/sh\necho "$GIT_NO_LAZY_FETCH $GIT_ALLOW_PROTOCOL" >> "%s/git-env"\nexec "%s" "$@"\n' "$shim" "$real_git" > "$shim/git"
chmod +x "$shim/git"
gates PATH="$shim:$PATH" bash "$p/scripts/publish.sh" >/dev/null 2>&1 || true
[ -s "$shim/git-env" ] && ! grep -qvx '1 none' "$shim/git-env" && ok "gates-only runs git with lazy fetch off and no transport" \
  || bad "git ran with an environment that could fetch" "$(sort -u "$shim/git-env" 2>/dev/null)"
# a scanner that cannot finish is not a clean result (also for a real export)
gshim="$work/gshim"; mkdir -p "$gshim"
real_grep="$(command -v grep)"
printf '#!/bin/sh\ncase "$*" in *-rIonE*) exit 2 ;; esac\nexec "%s" "$@"\n' "$real_grep" > "$gshim/grep"
chmod +x "$gshim/grep"
expect "gates-only: a pointer scan that errors is not clean" 1 "searching the staged tree failed" gates PATH="$gshim:$PATH" bash "$p/scripts/publish.sh"
p="$(make_private p5-go3)"; t="$(make_target p5-go3)"
expect "a real export: a pointer scan that errors is not clean" 1 "searching the staged tree failed" env PATH="$gshim:$PATH" PUBLISH_TEST_TARGET=1 bash "$p/scripts/publish.sh" "$t"
p="$(make_private p5-go1)"
echo x > "$p/internal/zz.xyz"; commit_all "$p"
expect "gates-only refuses a file kind the export does not allow" 1 "not a kind of file" gates bash "$p/scripts/publish.sh"
p="$(make_private p5-go2)"
printf 'package x\n// see %s/notes/a.md\n' "$dd" > "$p/internal/zz.go"; commit_all "$p"
expect "gates-only refuses a pointer to a docs/ file" 1 "points at docs/ files" gates bash "$p/scripts/publish.sh"
p="$(make_private p5-go4)"
echo "// about $deny_word" >> "$p/cmd/sitboard/main.go"; commit_all "$p"
expect "gates-only refuses a deny-list match in the export (privacy gate ran)" 1 "the public tree failed check-private" gates bash "$p/scripts/publish.sh"
[[ "$LAST_OUT" != *orblaxian* ]] && ok "gates-only never prints the word" || bad "gates-only printed the word" "$LAST_OUT"
printf 'exit 0\n' > "$p/scripts/check-private.sh"   # the worktree's scanner says clean; the committed one decides
expect "gates-only runs the committed scanner, not the worktree's" 1 "the public tree failed check-private" gates bash "$p/scripts/publish.sh"
# the staged copy of .gitignore and of the scanner decide, not the worktree's
p="$(make_private p5-go5)"
echo 'internal/zz.json' >> "$p/.gitignore"; echo '{}' > "$p/internal/zz.json"; git -C "$p" add -f internal/zz.json; commit_all "$p"
: > "$p/.gitignore"   # the worktree no longer ignores it; the committed rules do
expect "gates-only judges the committed ignore rules, not the worktree's" 1 "ignores" gates bash "$p/scripts/publish.sh"
p="$(make_private p5-ign)"; t="$(make_target p5-ign)"
echo '{}' > "$p/internal/server/dist/app.json"; git -C "$p" add -f internal/server/dist/app.json; commit_all "$p"
expect "export refuses a tracked file that .gitignore ignores" 1 "that .gitignore ignores" rehearse "$p/scripts/publish.sh" "$t"

# Each search, and the reading of its result, fails closed on its own: an
# error in the website-folder scan (the second one), or in the sed that reads
# a result, is not a clean answer either.
p="$(make_private p5-go7)"
gshim2="$work/gshim2"; mkdir -p "$gshim2"
printf '%s\n' '#!/bin/sh' 'case "$*" in *-rIonE*"|/)*"*) exit 2 ;; esac' "exec \"$real_grep\" \"\$@\"" > "$gshim2/grep"
chmod +x "$gshim2/grep"
expect "gates-only: a website-folder scan that errors is not clean" 1 "searching the staged tree failed" gates PATH="$gshim2:$PATH" bash "$p/scripts/publish.sh"
sshim="$work/sshim"; mkdir -p "$sshim"
printf '%s\n' '#!/bin/sh' 'case "$1" in "s|^\./||") exit 1 ;; esac' "exec \"$(command -v sed)\" \"\$@\"" > "$sshim/sed"
chmod +x "$sshim/sed"
expect "gates-only: a search result that cannot be read is not clean" 1 "could not read the search result" gates PATH="$sshim:$PATH" bash "$p/scripts/publish.sh"
# A gates-only run judges the staged snapshot alone: an ignore rule or a
# scanner deletion only in the worktree does not refuse it; a staged snapshot
# without the scanner does.
p="$(make_private p5-go6)"
echo 'cmd/sitboard/main.go' >> "$p/.gitignore"; rm "$p/scripts/check-private.sh"
expect "gates-only is not refused by an unstaged ignore rule or an unstaged scanner deletion" 0 "gates passed" gates bash "$p/scripts/publish.sh"
git -C "$p" rm -q --cached scripts/check-private.sh
expect "gates-only refuses a staged snapshot without the scanner" 1 "the staged snapshot has no scripts/check-private.sh" gates bash "$p/scripts/publish.sh"
# A partial clone: an exported blob that is not local fails the gate, and git
# does not fetch it on demand (git would fetch over file:// in these tests).
p="$(make_private p5-pc)"
pcb="$work/pc.git"; git clone -q --bare "$p" "$pcb"
git -C "$pcb" config uploadpack.allowFilter true; git -C "$pcb" config uploadpack.allowAnySHA1InWant true
pc="$work/pc"; git clone -q --filter=blob:none "file://$pcb" "$pc"
printf 'package x\n' > "$p/internal/zz2.go"; commit_all "$p"; git -C "$p" push -q "$pcb" main
git -C "$pc" fetch -q origin
pcblob="$(git -C "$pc" rev-parse origin/main:internal/zz2.go)"
GIT_NO_LAZY_FETCH=1 git -C "$pc" reset -q --soft origin/main && GIT_NO_LAZY_FETCH=1 git -C "$pc" read-tree origin/main
if GIT_NO_LAZY_FETCH=1 git -C "$pc" cat-file -e "$pcblob" 2>/dev/null; then
  bad "the partial clone has the new blob locally (the case cannot run)" ""
else
  expect "gates-only: an exported blob missing from a partial clone fails the gate" 1 "could not read the size of internal/zz2.go" \
    gates bash "$pc/scripts/publish.sh"
  GIT_NO_LAZY_FETCH=1 git -C "$pc" cat-file -e "$pcblob" 2>/dev/null && bad "gates-only fetched the missing blob" "" \
    || ok "gates-only fetched nothing into the partial clone"
fi
# A SIGTERM during a gates-only run removes its scratch folder, and the
# scanner's temporary folder with it (TMPDIR inside the scratch folder), even
# when the whole process group is signalled and the scanner dies at once.
# No case depends on timing: each stand-in blocks at its ready point until
# the test has sent the signal (for up to an hour, so a lost signal cannot hang
# the suite forever; the test always sends it, after at most 120 s).
# wait_for <file>: until it exists (at most about 120 s).
wait_for() { local i; for i in $(seq 1200); do [ -e "$1" ] && return 0; sleep 0.1; done; return 1; }
p="$(make_private p5-sig)"
sigshim="$work/sigshim"; mkdir -p "$sigshim" "$work/sgtmp"
# a grep that, for the pointer scan, says it is there and waits for the go
printf '%s\n' '#!/bin/sh' "case \"\$*\" in *-rIonE*) touch \"$work/sig-scan\"; i=0; while [ ! -e \"$work/sig-go\" ] && [ \$i -lt 36000 ]; do sleep 0.1; i=\$((i + 1)); done ;; esac" \
  "exec \"$real_grep\" \"\$@\"" > "$sigshim/grep"
chmod +x "$sigshim/grep"
# (env, not the gates function: a backgrounded function is a subshell, and
# $! would be that subshell, not publish.sh)
env PUBLISH_GATES_ONLY=1 PRIVATE_FLAGS= TMPDIR="$work/sgtmp" PATH="$sigshim:$PATH" bash "$p/scripts/publish.sh" >/dev/null 2>&1 & sigpid=$!
wait_for "$work/sig-scan"; inflight="$(ls -A "$work/sgtmp")"; kill -TERM "$sigpid"
touch "$work/sig-go"   # bash runs the trap once the grep it waits for returns
wait "$sigpid" 2>/dev/null; rc=$?
[ -n "$inflight" ] && [ "$rc" = 143 ] && [ -z "$(ls -A "$work/sgtmp")" ] && ok "a SIGTERM during gates-only exits 143 and leaves no scratch folder" \
  || bad "a SIGTERM during gates-only" "exit $rc, want 143; during the run: ${inflight:-nothing}; left in TMPDIR: $(ls -A "$work/sgtmp")"
p="$(make_private p5-sig2)"
# a stand-in scanner that makes its folder as the real one does, then waits
# until the signal ends it
printf '%s\n' '#!/usr/bin/env bash' 'd="$(mktemp -d "${TMPDIR:-/tmp}/check-private.XXXXXX")" || exit 2' 'trap '"'"'rm -rf "$d"'"'"' EXIT' \
  "touch \"$work/sig-cp\"" 'i=0; while [ $i -lt 36000 ]; do sleep 0.1; i=$((i + 1)); done' > "$p/scripts/check-private.sh"
commit_all "$p"
mkdir -p "$work/sgtmp2"
perl -e 'setpgrp(0, 0); exec { $ARGV[0] } @ARGV or die "exec $ARGV[0]: $!\n"' \
  env PUBLISH_GATES_ONLY=1 PRIVATE_FLAGS= TMPDIR="$work/sgtmp2" bash "$p/scripts/publish.sh" >/dev/null 2>&1 & sigpid=$!
wait_for "$work/sig-cp"; inflight="$(ls -A "$work"/sgtmp2/*/tmp 2>/dev/null)"; kill -TERM -- "-$sigpid"; wait "$sigpid" 2>/dev/null; rc=$?
[ -n "$inflight" ] && [ "$rc" = 143 ] && [ -z "$(ls -A "$work/sgtmp2")" ] && ok "a SIGTERM to the group during the scan leaves neither scratch folder" \
  || bad "a SIGTERM to the group during the scan" "exit $rc, want 143; scanner folder during the run: ${inflight:-none}; left in TMPDIR: $(ls -A "$work/sgtmp2")"
# and the real scanner does make its temporary folder inside the scratch folder
mshim="$work/mshim"; mkdir -p "$mshim"
printf '%s\n' '#!/bin/sh' "printf '%s\\n' \"\$*\" >> \"$work/mktemp-calls\"" "exec \"$(command -v mktemp)\" \"\$@\"" > "$mshim/mktemp"
chmod +x "$mshim/mktemp"
p="$(make_private p5-sig3)"
expect "gates-only with a mktemp that logs" 0 "gates passed" gates PATH="$mshim:$PATH" bash "$p/scripts/publish.sh"
grep -Eq '^-d /.*/publish\.[^/]+/tmp/check-private\.XXXXXX$' "$work/mktemp-calls" && ok "the scanner's temporary folder is inside publish.sh's scratch folder" \
  || bad "the scanner's temporary folder is outside the scratch folder" "$(cat "$work/mktemp-calls" 2>/dev/null)"

# An exported path with a control character is refused up front (every list
# of paths is one per line). The path lives in the index only.
p="$(make_private p5-nl)"; t="$(make_target p5-nl)"
blob="$(echo 'export const z = 3;' | git -C "$p" hash-object -w --stdin)"
git -C "$p" update-index --add --cacheinfo "100644,$blob,web/a"$'\n'"b.ts"
git -C "$p" -c user.name=test -c user.email=test@example.com -c commit.gpgsign=false commit -q -m 'a path with a newline'
expect "a real export refuses a path with a newline" 1 "a control character in the path" rehearse "$p/scripts/publish.sh" "$t"
[ -z "$(git -C "$t" ls-files)" ] && ok "the refused newline path left the target empty" || bad "files in the target" "$(git -C "$t" ls-files)"
expect "gates-only refuses a path with a newline" 1 "a control character in the path" gates bash "$p/scripts/publish.sh"
# The traps are set before the scratch folder exists: a setup step that fails
# right after it was made leaves nothing behind.
p="$(make_private p5-mk)"
mkshim="$work/mkshim"; mkdir -p "$mkshim" "$work/t19"
printf '%s\n' '#!/bin/sh' 'case "$*" in */publish.*/tmp) exit 1 ;; esac' "exec \"$(command -v mkdir)\" \"\$@\"" > "$mkshim/mkdir"
chmod +x "$mkshim/mkdir"
expect "gates-only: a failed scratch setup fails" 1 "could not create a scratch folder" gates TMPDIR="$work/t19" PATH="$mkshim:$PATH" bash "$p/scripts/publish.sh"
[ -z "$(ls -A "$work/t19")" ] && ok "the failed setup left no scratch folder" || bad "the failed setup left a scratch folder" "$(ls -A "$work/t19")"
# A relative TMPDIR is made absolute against the caller's folder.
mkdir -p "$work/t19r"
expect "gates-only with a relative TMPDIR" 0 "gates passed" bash -c 'cd "$1" && env PUBLISH_GATES_ONLY=1 PRIVATE_FLAGS= TMPDIR=t19r bash "$2"' _ "$work" "$p/scripts/publish.sh"
[ -z "$(ls -A "$work/t19r")" ] && ok "the relative TMPDIR run cleaned up after itself" || bad "the relative TMPDIR run left files" "$(ls -A "$work/t19r")"
# Gates-only reads RELEASE_VERSION from the staged Makefile, not HEAD's.
p="$(make_private p5-ver)"
sed -i.bak "s/^RELEASE_VERSION := .*/RELEASE_VERSION := next/" "$p/Makefile"; rm -f "$p/Makefile.bak"; commit_all "$p"
sed -i.bak "s/^RELEASE_VERSION := .*/RELEASE_VERSION := $V/" "$p/Makefile"; rm -f "$p/Makefile.bak"; git -C "$p" add Makefile
expect "gates-only accepts a staged version over a bad committed one" 0 "gates passed" gates bash "$p/scripts/publish.sh"
commit_all "$p"
sed -i.bak "s/^RELEASE_VERSION := .*/RELEASE_VERSION := next/" "$p/Makefile"; rm -f "$p/Makefile.bak"; git -C "$p" add Makefile
expect "gates-only refuses a bad staged version over a good committed one" 1 "RELEASE_VERSION in the Makefile is 'next'" gates bash "$p/scripts/publish.sh"

# A signal that arrives while the scratch folder is being made (here: right
# after mktemp made it, before its name is in hand) still removes it.
p="$(make_private p5-mt)"
mtshim="$work/mtshim"; mkdir -p "$mtshim" "$work/mttmp"
printf '%s\n' '#!/bin/sh' "case \"\$*\" in */publish.XXXXXX) out=\"\$(\"$(command -v mktemp)\" \"\$@\")\" || exit 1; printf '%s\\n' \"\$out\"; kill -TERM \"\$PPID\"; exit 0 ;; esac" \
  "exec \"$(command -v mktemp)\" \"\$@\"" > "$mtshim/mktemp"
chmod +x "$mtshim/mktemp"
expect "a SIGTERM while the scratch folder is made exits 143" 143 "-" gates TMPDIR="$work/mttmp" PATH="$mtshim:$PATH" bash "$p/scripts/publish.sh"
[ -z "$(ls -A "$work/mttmp")" ] && ok "and leaves no scratch folder" || bad "a SIGTERM while the scratch folder is made left it" "$(ls -A "$work/mttmp")"

# P1b. The banner is exported by its exact name: another picture in .github is
# refused even when someone adds it to the allowlist.
p="$(make_private p1b)"; t="$(make_target p1b)"
sed -i.bak 's|^  \.github/sitboard-banner\.svg$|  .github/sitboard-banner.svg .github/draft-banner.svg|' "$p/scripts/publish.sh"
rm -f "$p/scripts/publish.sh.bak"; commit_all "$p"
grep -q 'draft-banner.svg' "$p/scripts/publish.sh" || bad "P1b did not plant the second picture in the allowlist" ""
expect "another .github picture is refused even when allowlisted" 1 "draft-banner.svg: not a kind of file this folder exports" rehearse "$p/scripts/publish.sh" "$t"

# P1c. A file named in the allowlist below the top level is one file: a folder
# of that name (git matches a path as a folder prefix too) is refused, for the
# banner and for the workflow, whatever is inside.
for named in .github/sitboard-banner.svg .github/workflows/ci.yml; do
  p="$(make_private "p1c-${named##*/}")"; t="$(make_target "p1c-${named##*/}")"
  rm -f "$p/$named"; mkdir -p "$p/$named"
  echo 'name: evil' > "$p/$named/evil.yml"; echo '<svg xmlns="http://www.w3.org/2000/svg"/>' > "$p/$named/sitboard-banner.svg"
  commit_all "$p"
  before="$(state "$t")"
  expect "a folder named $named is refused" 1 "allowlisted file is not one tracked file: $named" rehearse "$p/scripts/publish.sh" "$t"
  [[ "$(state "$t")" == "$before" ]] && ok "the refused folder $named left nothing in the target" || bad "the folder $named touched the target" ""
done
# P1d. A "=" kind is an exact path below its folder: a banner of the same name
# deeper in .github is refused even when someone allowlists it.
p="$(make_private p1d)"; t="$(make_target p1d)"
mkdir -p "$p/.github/x"; echo '<svg xmlns="http://www.w3.org/2000/svg"/>' > "$p/.github/x/sitboard-banner.svg"
sed -i.bak 's|^  \.github/sitboard-banner\.svg$|  .github/sitboard-banner.svg .github/x/sitboard-banner.svg|' "$p/scripts/publish.sh"
rm -f "$p/scripts/publish.sh.bak"; commit_all "$p"
grep -q '\.github/x/sitboard-banner\.svg' "$p/scripts/publish.sh" || bad "P1d did not plant the deeper banner in the allowlist" ""
expect "a same-named banner deeper in .github is refused" 1 ".github/x/sitboard-banner.svg: not a kind of file this folder exports" rehearse "$p/scripts/publish.sh" "$t"

# P1e. The banner is plain drawing only: script in any spelling (a prefixed
# element, an event attribute, an entity, a newline or a tab inside a tag),
# links, external paint, embedded images and HTML, metadata, comments, data
# URLs, editor residue, CSS, any element or attribute beyond the listed ones
# and an unquoted value are refused, and the target is left as it was.
svg_bad=(
  '<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>'
  '<svg xmlns="http://www.w3.org/2000/svg"><a href="https://example.com"><rect width="1" height="1"/></a></svg>'
  '<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"><use xlink:HREF="#a"/></svg>'
  '<svg xmlns="http://www.w3.org/2000/svg"><IMAGE width="1" height="1"/></svg>'
  '<svg xmlns="http://www.w3.org/2000/svg"><foreignObject><p>hi</p></foreignObject></svg>'
  '<svg xmlns="http://www.w3.org/2000/svg"><metadata>who made it</metadata></svg>'
  '<svg xmlns="http://www.w3.org/2000/svg"><!-- a note --></svg>'
  '<svg xmlns="http://www.w3.org/2000/svg"><rect fill="url(data:image/png;base64,AAAA)"/></svg>'
  '<svg xmlns="http://www.w3.org/2000/svg" xmlns:sodipodi="http://sodipodi.sourceforge.net/DTD/sodipodi-0.dtd"/>'
  '<svg xmlns="http://www.w3.org/2000/svg" xmlns:inkscape="http://www.inkscape.org/namespaces/inkscape"/>'
  '<svg xmlns="http://www.w3.org/2000/svg"><rect Style="fill:red"/></svg>'
  '<svg xmlns="http://www.w3.org/2000/svg" xmlns:s="http://www.w3.org/2000/svg"><s:script>alert(1)</s:script></svg>'
  '<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>'
  '<!DOCTYPE svg [<!ENTITY s "&#60;s&#99;ript&#62;alert(1)&#60;/s&#99;ript&#62;">]><svg xmlns="http://www.w3.org/2000/svg">&s;</svg>'
  '<svg xmlns="http://www.w3.org/2000/svg"><rect fill="url(https://example.com/x.svg#g)"/></svg>'
  '<svg xmlns="http://www.w3.org/2000/svg"><circle r="1"/></svg>'
  $'<svg xmlns="http://www.w3.org/2000/svg"\nonload\n="document.documentElement.setAttribute(\'data-ran\',\'x\')"><rect width="9" height="9"/></svg>'
  $'<svg xmlns="http://www.w3.org/2000/svg"><rect\nxml:base="https://x/" width="1"/></svg>'
  '<svg xmlns="http://www.w3.org/2000/svg"><rect OnClick="alert(1)" width="1"/></svg>'
  '<svg xmlns="http://www.w3.org/2000/svg"><rect id="a" width="1"/></svg>'
  '<svg xmlns="http://www.w3.org/2000/svg"><rect width=1 height="1"/></svg>'
  $'<svg xmlns="http://www.w3.org/2000/svg"><rect\tonclick="x" width="1"/></svg>'
)
for i in "${!svg_bad[@]}"; do
  p="$(make_private "p1e-$i")"; t="$(make_target "p1e-$i")"
  printf '%s\n' "${svg_bad[$i]}" > "$p/.github/sitboard-banner.svg"; commit_all "$p"
  before="$(state "$t")"
  expect "a banner with refused content is refused ($i)" 1 ".github/sitboard-banner.svg: an SVG that is not plain drawing" \
    rehearse "$p/scripts/publish.sh" "$t"
  [[ "$(state "$t")" == "$before" ]] || bad "the refused banner ($i) touched the target" ""
done
# A NUL byte past git's 8000-byte binary window, then an unlisted element:
# refused on every awk (macOS awk ends a record at the NUL).
p="$(make_private p1e-nul)"; t="$(make_target p1e-nul)"
{ printf '<svg xmlns="http://www.w3.org/2000/svg"><text x="1">'; head -c 8100 /dev/zero | LC_ALL=C tr '\0' 'a'
  printf '\000</text><iframe xmlns="http://www.w3.org/1999/xhtml" src="https://t.example/"></iframe></svg>\n'; } > "$p/.github/sitboard-banner.svg"
commit_all "$p"
before="$(state "$t")"
expect "a banner with a NUL byte past the binary window is refused" 1 ".github/sitboard-banner.svg: an SVG that is not plain drawing" \
  rehearse "$p/scripts/publish.sh" "$t"
[[ "$(state "$t")" == "$before" ]] || bad "the banner with a NUL byte touched the target" ""
# The real banner passes the same gate, and exports as it is.
p="$(make_private p1e-real)"; t="$(make_target p1e-real)"
cp "$repo/.github/sitboard-banner.svg" "$p/.github/sitboard-banner.svg"; commit_all "$p"
expect "the README banner is plain drawing and exports" 0 "staged only" rehearse "$p/scripts/publish.sh" "$t"
cmp -s "$repo/.github/sitboard-banner.svg" "$t/.github/sitboard-banner.svg" \
  && ok "the exported banner is the README banner, byte for byte" || bad "the exported banner differs from the README banner" ""
# P1f. An allowlisted file below the top level with a non-ASCII name is one
# file like any other (git quotes such names unless asked for raw bytes).
p="$(make_private p1f)"; t="$(make_target p1f)"
echo 'name: other' > "$p/.github/workflows/caf"$'\xc3\xa9'".yml"
sed -i.bak 's|^  \.github/workflows/ci\.yml$|  .github/workflows/ci.yml .github/workflows/caf'$'\xc3\xa9''.yml|' "$p/scripts/publish.sh"
rm -f "$p/scripts/publish.sh.bak"; commit_all "$p"
grep -q 'workflows/caf' "$p/scripts/publish.sh" || bad "P1f did not plant the non-ASCII name in the allowlist" ""
expect "an allowlisted file with a non-ASCII name exports" 0 "staged only" rehearse "$p/scripts/publish.sh" "$t"

# P6 (review 3). The target's own ignore rules cannot drop an exported file.
p="$(make_private p6)"; t="$(make_target p6)"
printf 'LICENSE\nCHANGELOG.md\n' > "$t/.git/info/exclude"
expect "an export into a target that ignores files" 0 "staged only" rehearse "$p/scripts/publish.sh" "$t"
git -C "$t" ls-files --error-unmatch LICENSE CHANGELOG.md >/dev/null 2>&1 \
  && [[ "$(git -C "$t" ls-files | wc -l)" == "$(git -C "$p" ls-files -- cmd internal web scripts Makefile go.mod go.sum README.md LICENSE THIRD_PARTY_NOTICES.md CHANGELOG.md .github/workflows/ci.yml .github/sitboard-banner.svg .gitignore testdata test .golangci.yml notices.go notices_test.go | wc -l)" ]] \
  && ok "the target's ignore rules dropped nothing" || bad "files dropped by the target's ignore rules" "$(git -C "$t" ls-files | wc -l)"

# Review 2.9: a non-ASCII file name exports like any other.
p="$(make_private p6u)"; t="$(make_target p6u)"
echo 'export const x = 2;' > "$p/web/src/caf"$'\xc3\xa9'".ts"; commit_all "$p"
expect "an export with a non-ASCII file name" 0 "staged only" rehearse "$p/scripts/publish.sh" "$t"
git -C "$t" -c core.quotePath=false ls-files | grep -q "caf"$'\xc3\xa9'".ts" && ok "the non-ASCII file is staged" || bad "non-ASCII file missing" ""
# Review 2.8: the target's own config cannot run a program.
p="$(make_private p6f)"; t="$(make_target p6f)"
printf '#!/bin/sh\ntouch "%s"\n' "$work/fsmonitor-ran" > "$work/fsmonitor.sh"; chmod +x "$work/fsmonitor.sh"
git -C "$t" config core.fsmonitor "$work/fsmonitor.sh"
expect "an export into a target with an fsmonitor hook" 0 "staged only" rehearse "$p/scripts/publish.sh" "$t"
[[ ! -e "$work/fsmonitor-ran" ]] && ok "the target's fsmonitor program never ran" || bad "the fsmonitor program ran" ""

# Review 3.8: an interrupted write puts the target back; a refused export
# does not even update the target's tracking ref.
p="$(make_private p6i)"; t="$(seeded_target p6i)"
before="$(state "$t")"
# The case proves that an interrupt stops the run with 130 and puts the target
# back as it was. publish.sh interrupts itself with SIGINT. A non-interactive
# shell starts its background jobs with SIGINT ignored, and a signal ignored
# at start stays ignored (POSIX): run as `scripts/release_test.sh &`, the
# interrupt would do nothing. perl puts SIGINT back to its default before
# publish.sh starts, so the case runs the same in the foreground or not.
with_sigint() { perl -e '$SIG{INT} = "DEFAULT"; exec { $ARGV[0] } @ARGV or die "exec $ARGV[0]: $!\n"' "$@"; }
expect "an interrupted write stops the run" 130 "put back as it was" \
  with_sigint env PUBLISH_TEST_INTERRUPT_AFTER=3 PUBLISH_TEST_TARGET=1 bash "$p/scripts/publish.sh" "$t"
[[ "$(state "$t")" == "$before" ]] && ok "the interrupted write left the target as it was" || bad "interrupted write changed the target" "$(git -C "$t" status --short | head)"
p="$(make_private p6t)"; t="$(make_remote_target p6t)"; b6t="$work/bare-p6t.git"
seed="$work/seed-p6t"; git init -q -b main "$seed"; echo old > "$seed/OLD.md"; commit_all "$seed" "old"
git -C "$seed" push -q "$b6t" HEAD:refs/heads/main
git -C "$t" fetch -q "$b6t" main && git -C "$t" reset -q --hard FETCH_HEAD
echo "// about $deny_word" >> "$p/cmd/sitboard/main.go"; commit_all "$p"
before="$(state "$t")"
expect "a refused export in a commit rehearsal" 1 "the public tree failed check-private" \
  env PUBLISH_TEST_REMOTE="$b6t" PUBLISH_IDENT="$IDENT" bash "$p/scripts/publish.sh" "$t" --update "x"
[[ "$(state "$t")" == "$before" ]] && ok "a refused export leaves the target's refs and objects as they were" || bad "refused export changed the target" "$(git -C "$t" for-each-ref)"
# The clean copy's make check runs export-gates, as the real Makefile does:
# the outer run's rehearsal switches must not reach that nested publish.sh, or
# a commit rehearsal and an interruption rehearsal would both stop at the
# clean copy instead of reaching the write.
nested_mk='RELEASE_VERSION := %s\n\ncheck:\n\tPUBLISH_GATES_ONLY=1 PRIVATE_FLAGS="$(PRIVATE_FLAGS)" bash scripts/publish.sh\n\nbuild:\n\t@echo built\n'
p="$(make_private p6n)"; t="$(make_remote_target p6n)"
printf "$nested_mk" "$V" > "$p/Makefile"; commit_all "$p"
expect "a commit rehearsal whose clean copy runs export-gates commits" 0 "-" \
  env PUBLISH_TEST_REMOTE="$work/bare-p6n.git" PUBLISH_IDENT="$IDENT" bash "$p/scripts/publish.sh" "$t" --update "Tooling"
[[ "$(git -C "$t" log -1 --format=%s 2>/dev/null)" == Tooling ]] && ok "the nested-gates commit rehearsal made its commit" \
  || bad "the nested-gates commit rehearsal made no commit" "$LAST_OUT"
p="$(make_private p6n2)"; t="$(seeded_target p6n2)"
printf "$nested_mk" "$V" > "$p/Makefile"; commit_all "$p"
before="$(state "$t")"
expect "an interruption rehearsal whose clean copy runs export-gates reaches the write" 130 "put back as it was" \
  with_sigint env PUBLISH_TEST_INTERRUPT_AFTER=3 PUBLISH_TEST_TARGET=1 bash "$p/scripts/publish.sh" "$t"
[[ "$(state "$t")" == "$before" ]] && ok "that interrupted write left the target as it was" || bad "that interrupted write changed the target" "$(git -C "$t" status --short | head)"
# A rollback that cannot restore the target's index says so, keeps the list
# of written files and the scratch folder, and does not claim success.
p="$(make_private p6r)"; t="$(seeded_target "p6r with space")"   # recover.sh must quote the path
before="$(state "$t")"
rshim="$work/rshim"; mkdir -p "$rshim" "$work/rtmp"
printf '%s\n' '#!/bin/sh' 'case " $* " in *" read-tree "*) case " $* " in *" --empty "*) ;; *) exit 1 ;; esac ;; esac' \
  "exec \"$real_git\" \"\$@\"" > "$rshim/git"
chmod +x "$rshim/git"
expect "a rollback that fails says the target could not be put back" 130 "could NOT be put back" \
  with_sigint env PATH="$rshim:$PATH" TMPDIR="$work/rtmp" PUBLISH_TEST_INTERRUPT_AFTER=3 PUBLISH_TEST_TARGET=1 bash "$p/scripts/publish.sh" "$t"
kept="$(ls "$work"/rtmp/*/written 2>/dev/null)"
[[ "$LAST_OUT" != *"put back as it was"* && -s "$kept" ]] && ok "the failed rollback claims nothing and keeps the list of written files" \
  || bad "the failed rollback" "kept: ${kept:-nothing}
$LAST_OUT"
rec="$(ls "$work"/rtmp/*/recover.sh 2>/dev/null)"
[[ "$LAST_OUT" == *"finish by hand: bash '"*"/recover.sh'"* && -n "$rec" ]] && bash "$rec" >/dev/null 2>&1 && [[ "$(state "$t")" == "$before" ]] \
  && ok "recover.sh, as printed, puts back a target whose path has a space" || bad "recover.sh after a failed read-tree" "recover: ${rec:-none}
$(git -C "$t" status --short | head)"
# A rollback that cannot remove a file it may have written says so and leaves
# recover.sh, which (run as printed, without the failing rm) puts it all back.
p="$(make_private p6rm)"; t="$(seeded_target p6rm)"
before="$(state "$t")"
rmshim="$work/rmshim"; mkdir -p "$rmshim" "$work/rmtmp"
# (.gitignore is among the first files written, so it is really there)
printf '%s\n' '#!/bin/sh' 'case "$*" in */.gitignore) exit 1 ;; esac' "exec \"$(command -v rm)\" \"\$@\"" > "$rmshim/rm"
chmod +x "$rmshim/rm"
expect "a rollback whose removal fails says the target could not be put back" 130 "could NOT be put back" \
  with_sigint env PATH="$rmshim:$PATH" TMPDIR="$work/rmtmp" PUBLISH_TEST_INTERRUPT_AFTER=3 PUBLISH_TEST_TARGET=1 bash "$p/scripts/publish.sh" "$t"
rec="$(ls "$work"/rmtmp/*/recover.sh 2>/dev/null)"
[[ "$LAST_OUT" != *"put back as it was"* && -n "$rec" ]] && bash "$rec" >/dev/null 2>&1 && [[ "$(state "$t")" == "$before" ]] \
  && ok "recover.sh puts the target back after a failed removal" || bad "recover.sh after a failed removal" "recover: ${rec:-none}
$(git -C "$t" status --short | head)"
# The rollback removes only paths the write began, and none through a link: a
# folder on the way that became a link to somewhere else is refused, and the
# file behind it stays. (The shims act on the target's paths and its restore
# only, not on the scratch stage or the clean copy.)
p="$(make_private p6ln)"; t="$(seeded_target p6ln)"
mkdir -p "$work/decoy-wf"; echo 'outside' > "$work/decoy-wf/ci.yml"
chshim="$work/chshim"; mkdir -p "$chshim" "$work/lntmp"
printf '%s\n' '#!/bin/sh' "case \"\$2\" in \"$t/.github/workflows/ci.yml\") d=\"\${2%/ci.yml}\"; \"$(command -v rm)\" -rf \"\$d\"; ln -s \"$work/decoy-wf\" \"\$d\"; exit 1 ;; esac" \
  "exec \"$(command -v chmod)\" \"\$@\"" > "$chshim/chmod"
chmod +x "$chshim/chmod"
expect "a rollback refuses to remove through a link" 1 "could NOT be put back" \
  env PATH="$chshim:$PATH" TMPDIR="$work/lntmp" PUBLISH_TEST_TARGET=1 bash "$p/scripts/publish.sh" "$t"
[ -f "$work/decoy-wf/ci.yml" ] && ok "the file behind the link is untouched" || bad "the rollback removed a file outside the target" "$LAST_OUT"
# A restore that fails part-way (an old link already back) is retried by
# recover.sh without replaying the removal, and recover.sh ignores the git
# environment of the shell it runs in.
p="$(make_private p6pc)"; t="$(seeded_target p6pc)"
mkdir -p "$work/decoy-cmd/sitboard"; echo 'outside' > "$work/decoy-cmd/sitboard/main.go"
ln -s "$work/decoy-cmd" "$t/cmd"; commit_all "$t" "a link"
before="$(state "$t")"
cishim="$work/cishim"; mkdir -p "$cishim" "$work/citmp"
printf '%s\n' '#!/bin/sh' "case \" \$* \" in *\" checkout-index -a -f \"*) \"$real_git\" \"\$@\"; exit 1 ;; esac" "exec \"$real_git\" \"\$@\"" > "$cishim/git"
chmod +x "$cishim/git"
expect "a restore that fails after putting an old link back" 130 "could NOT be put back" \
  with_sigint env PATH="$cishim:$PATH" TMPDIR="$work/citmp" PUBLISH_TEST_INTERRUPT_AFTER=10 PUBLISH_TEST_TARGET=1 bash "$p/scripts/publish.sh" "$t"
[ -L "$t/cmd" ] && ok "(the old link is back before the failure)" || bad "the partial restore did not put the link back" "$(ls -la "$t")"
other="$work/other-checkout"; git init -q -b main "$other"; echo keep > "$other/keep.txt"; commit_all "$other" "other"
before_other="$(state "$other")"
rec="$(ls "$work"/citmp/*/recover.sh 2>/dev/null)"
expect "recover.sh retries the restore alone, whatever GIT_DIR and GIT_WORK_TREE say" 0 "the target was put back" \
  env GIT_DIR="$other/.git" GIT_WORK_TREE="$other" bash "${rec:-/nonexistent}"
[[ "$(state "$t")" == "$before" && "$(state "$other")" == "$before_other" && -f "$work/decoy-cmd/sitboard/main.go" ]] \
  && ok "the target is back, the other checkout and the file behind the link untouched" \
  || bad "recover.sh after a partial restore" "$(git -C "$t" status --short | head)"
# recover.sh refuses a target that is no longer the same checkout.
p="$(make_private p6id)"; t="$(seeded_target p6id)"
mkdir -p "$work/idtmp"
expect "a failed rollback for the identity case" 130 "could NOT be put back" \
  with_sigint env PATH="$rshim:$PATH" TMPDIR="$work/idtmp" PUBLISH_TEST_INTERRUPT_AFTER=3 PUBLISH_TEST_TARGET=1 bash "$p/scripts/publish.sh" "$t"
mv "$t" "$t.moved"; git clone -q "$t.moved" "$t"
before="$(state "$t")"
rec="$(ls "$work"/idtmp/*/recover.sh 2>/dev/null)"
expect "recover.sh refuses a checkout that was replaced at the same path" 1 "is not the checkout publish.sh wrote to" bash "${rec:-/nonexistent}"
[[ "$(state "$t")" == "$before" ]] && ok "the replacement checkout is untouched" || bad "recover.sh changed the replacement" "$(git -C "$t" status --short | head)"
# Every scanner and the clean copy keep their temporary folders inside the
# scratch folder, in a commit rehearsal too (tree, message and history scans).
p="$(make_private p6tmp)"; t="$(make_remote_target p6tmp)"
printf 'RELEASE_VERSION := %s\n\ncheck:\n\t@echo "clean copy TMPDIR=$$TMPDIR"\n\nbuild:\n\t@echo built\n' "$V" > "$p/Makefile"; commit_all "$p"
: > "$work/mktemp-calls"
expect "a commit rehearsal with a mktemp that logs" 0 "-" \
  env PATH="$mshim:$PATH" PUBLISH_TEST_REMOTE="$work/bare-p6tmp.git" PUBLISH_IDENT="$IDENT" bash "$p/scripts/publish.sh" "$t" --update "Tooling"
cp_calls="$(grep -c 'check-private' "$work/mktemp-calls")"
cp_inside="$(grep -cE '^-d /.*/publish\.[^/]+/tmp/check-private\.XXXXXX$' "$work/mktemp-calls")"
[[ "$cp_calls" -ge 3 && "$cp_calls" == "$cp_inside" ]] && ok "the tree, message and history scans all keep their folders in the scratch folder" \
  || bad "a scanner's folder is outside the scratch folder" "$(cat "$work/mktemp-calls")"
[[ "$LAST_OUT" =~ clean\ copy\ TMPDIR=/[^[:space:]]*/publish\.[^/[:space:]]+/tmp ]] && ok "the clean copy's TMPDIR is inside the scratch folder" \
  || bad "the clean copy's TMPDIR" "$LAST_OUT"

# P7 (review 4, 5). The target is never the private repository, inside it,
# around it, a worktree or clone of it, or a checkout with uncommitted work,
# however the path is spelled; a relative target is the caller's.
p="$(make_private p7)"
expect "a target that is the private repository is refused" 1 "inside the private repository" rehearse "$p/scripts/publish.sh" "$p"
git init -q -b main "$p/sub"
expect "a nested checkout inside the private repository is refused" 1 "inside the private repository" \
  rehearse "$p/scripts/publish.sh" "$p/sub"
rm -rf "$p/sub"
ln -s "$p" "$work/link-to-private"
expect "a symlink to the private repository is refused" 1 "inside the private repository" \
  rehearse "$p/scripts/publish.sh" "$work/link-to-private"
mkdir -p "$work/outer"; git init -q -b main "$work/outer"
inner="$(make_private p7in "$work/outer/inner")"
expect "a target that contains the private repository is refused" 1 "inside the target" \
  rehearse "$inner/scripts/publish.sh" "$work/outer"
[[ -f "$inner/README.md" ]] && ok "the private repository inside the target is untouched" || bad "inner repository damaged" ""
git clone -q "$p" "$work/sibling"
echo "unsaved edit" >> "$work/sibling/README.md"
expect "a clone of the private repository is refused" 1 "private repository's history" \
  rehearse "$p/scripts/publish.sh" "$work/sibling"
grep -q "unsaved edit" "$work/sibling/README.md" && [[ -d "$work/sibling/$dd" ]] \
  && ok "the sibling clone keeps its edit and its docs" || bad "sibling clone damaged" ""
git -C "$p" worktree add -q -b wt "$work/wt-private" 2>/dev/null
expect "a worktree of the private repository is refused" 1 "private repository" \
  rehearse "$p/scripts/publish.sh" "$work/wt-private"
git -C "$p" worktree remove --force "$work/wt-private"; git -C "$p" branch -q -D wt
if case_insensitive "$p"; then
  expect "another spelling of the private path is refused" 1 "inside the private repository" \
    rehearse "$p/scripts/publish.sh" "$(upper "$p")"
  expect "the script started through another spelling refuses its own repository" 1 "inside the private repository" \
    env PUBLISH_IDENT="$IDENT" bash "$(upper "$p")/scripts/publish.sh" "$p"
else
  echo "skip case-variant paths (case-sensitive file system)"
fi
t="$(make_target p7m)"
echo 'kept' > "$t/notes.md"; commit_all "$t" notes
echo 'uncommitted' >> "$t/notes.md"
expect "a target with uncommitted changes is refused" 1 "uncommitted changes" rehearse "$p/scripts/publish.sh" "$t"
grep -q uncommitted "$t/notes.md" && ok "the uncommitted change is kept" || bad "uncommitted change lost" ""
t="$(make_target p7s)"
echo 'kept' > "$t/notes.md"; commit_all "$t" notes
echo 'staged by hand' >> "$t/notes.md"; git -C "$t" add notes.md
expect "a target with staged changes publish.sh did not make is refused" 1 "did not make" rehearse "$p/scripts/publish.sh" "$t"
for flag in --assume-unchanged --skip-worktree; do
  t="$(make_target "p7h$flag")"
  echo 'kept' > "$t/notes.md"; commit_all "$t" notes
  echo 'hidden edit' >> "$t/notes.md"; git -C "$t" update-index "$flag" notes.md
  expect "a target with $flag on an edited file is refused" 1 "assume-unchanged or skip-worktree" rehearse "$p/scripts/publish.sh" "$t"
  grep -q 'hidden edit' "$t/notes.md" && ok "the hidden edit ($flag) is kept" || bad "hidden edit lost ($flag)" ""
done
t="$(make_target p7pu)"; git -C "$t" remote add origin "$work/somewhere.git"; git -C "$t" config remote.origin.pushurl "$PUBLIC_URL"
expect "a target with a push URL is refused" 1 "push URL" rehearse "$p/scripts/publish.sh" "$t"
t="$(make_target p7up)"; git -C "$t" remote add upstream "$PUBLIC_URL"
expect "a rehearsal refuses a checkout with the public repository as any remote" 1 "a remote that is the public repository" \
  rehearse "$p/scripts/publish.sh" "$t"
n=0
for ref in "../../$dd/spikes/1.md" "./$dd/spikes/1.md" "./$ss/index.html" "../$ss/index.html" "/$ss/x"; do
  n=$((n + 1))
  p_r="$(make_private "p7r-$n")"; t="$(make_target "p7r-$n")"
  printf '// see %s\n' "$ref" >> "$p_r/cmd/sitboard/main.go"; commit_all "$p_r"
  expect "a relative pointer $ref is refused" 1 "cmd/sitboard/main.go:" rehearse "$p_r/scripts/publish.sh" "$t"
done
t="$(make_target p7u)"
echo stray > "$t/stray.txt"
expect "a target with untracked files is refused" 1 "untracked or ignored files" rehearse "$p/scripts/publish.sh" "$t"
make_target p7rel >/dev/null
expect "a relative target is the caller's folder" 0 "staged only" \
  bash -c 'cd "$1" && PUBLISH_TEST_TARGET=1 bash "$2/scripts/publish.sh" public-p7rel' _ "$work" "$p"
[[ -n "$(git -C "$work/public-p7rel" ls-files)" && ! -e "$p/public-p7rel" ]] \
  && ok "the relative target got the export" || bad "relative target" "$(ls "$p")"
t="$(make_target p7o)"
git -C "$t" remote add origin "https://github.com/someone/fork.git"
expect "a target whose origin is not the public repository is refused" 1 "not the public repository" \
  env PUBLISH_IDENT="$IDENT" bash "$p/scripts/publish.sh" "$t"
expect "a stage-only rehearsal never commits" 1 "never commits" \
  env PUBLISH_TEST_TARGET=1 PUBLISH_IDENT="$IDENT" bash "$p/scripts/publish.sh" "$t" --commit
expect "unknown option" 2 "unknown option" rehearse "$p/scripts/publish.sh" "$t" --push

# P8 (review 7, 8). A commit rehearsal against a stand-in: never in a checkout
# whose origin is real, no push command, last-published untouched; the
# commit message is scanned, and a commit that fails is undone.
p="$(make_private p8)"; t="$(make_remote_target p8)"; b="$work/bare-p8.git"
real() { env PUBLISH_TEST_REMOTE="$b" PUBLISH_IDENT="$IDENT" bash "$p/scripts/publish.sh" "$t" "$@"; }
t_real="$(make_target p8real)"; git -C "$t_real" remote add origin "$PUBLIC_URL"
expect "a commit rehearsal refuses a checkout whose origin is the public URL" 1 "a remote that is the public repository" \
  env PUBLISH_TEST_REMOTE="$b" PUBLISH_IDENT="$IDENT" bash "$p/scripts/publish.sh" "$t_real" --commit
[[ -z "$(git -C "$t_real" ls-files)" ]] && ok "the real-origin checkout is untouched" || bad "real-origin checkout touched" ""
echo dirty >> "$p/README.md"
expect "a dirty private tree is refused" 1 "not clean" real
git -C "$p" checkout -q -- README.md
expect "committing needs PUBLISH_IDENT" 1 "PUBLISH_IDENT" \
  env PUBLISH_TEST_REMOTE="$b" bash "$p/scripts/publish.sh" "$t" --commit
expect "a release commits and tags v$V" 0 "tagged v$V" real --commit
[[ "$LAST_OUT" == *"history mode, all refs"* ]] \
  && ok "the gate's history scan reads every ref of the target, not a branch scope" \
  || bad "the gate's history scan is not the full one" "$LAST_OUT"
subject="$(git -C "$t" log -1 --format=%s)"
body="$(git -C "$t" log -1 --format=%b)"
who="$(git -C "$t" log -1 --format='%an <%ae>|%cn <%ce>')"
tagger="$(git -C "$t" for-each-ref refs/tags/v$V --format='%(taggername) %(taggeremail)')"
if [[ "$subject" == "sitboard $V" && "$body" == *"First release."* && "$body" != *Older* \
      && "$who" == "$IDENT|$IDENT" && "$tagger" == "$IDENT" ]]; then
  ok "the release commit and tag carry the changelog entry and the identity"
else
  bad "release commit" "subject=$subject who=$who tagger=$tagger
$body"
fi
[[ "$LAST_OUT" == *"REHEARSAL: no push command"* && "$LAST_OUT" != *"push with:"* ]] \
  && ok "a rehearsal prints no push command" || bad "rehearsal push command" "$LAST_OUT"
git -C "$p" rev-parse -q --verify last-published >/dev/null \
  && bad "a rehearsal moved last-published" "" || ok "a rehearsal leaves last-published alone"
git -C "$t" push -q "$b" HEAD:refs/heads/main "refs/tags/v$V"   # the manual push, to the stand-in
expect "a released version cannot be released again" 1 "already released" real --commit
expect "an update with no change has nothing to commit" 1 "nothing to commit" real --update "Docs"
echo "more words" >> "$p/README.md"; commit_all "$p" "readme"
before="$(state "$t")"
expect "an update whose subject holds a private word is refused before any commit" 1 "the commit message failed check-private" \
  real --update "notes for ${deny_word}"
[[ "$(state "$t")" == "$before" ]] && ok "the refused subject left the target untouched" || bad "refused subject touched the target" "$(git -C "$t" log --oneline -2)"
[[ "$LAST_OUT" != *orblaxian* ]] && ok "the message gate never prints the word" || bad "message gate printed the word" "$LAST_OUT"
git -C "$p" branch secret-plan
private_c="$(git -C "$p" rev-parse --short HEAD)"
for subject in "fix from $private_c" "work from secret-plan" "see $dd/spikes/1.md" "work from SECRET-PLAN" \
               "fix ${private_c:0:6}" "id abc${private_c}def"; do
  expect "an update subject naming private things is refused: $subject" 1 "the subject names" real --update "$subject"
done
[[ "$(state "$t")" == "$before" ]] && ok "the refused subjects left the target untouched" || bad "refused subjects touched the target" ""
git -C "$t" config i18n.commitEncoding ISO-8859-1
expect "an update commits without a tag" 0 "(no tag)" real --update "README: more words"
[[ "$(git -C "$t" log -1 --format=%s)" == "README: more words" && "$(git -C "$t" tag | wc -l | tr -d ' ')" == 1 ]] \
  && ok "the update commit has the subject and no new tag" || bad "update commit" "$(git -C "$t" log --oneline)"
git -C "$t" cat-file commit HEAD | grep -q '^encoding ' && bad "the target's commitEncoding added a header" "" || ok "the commit has no encoding header"
git -C "$t" push -q "$b" HEAD:refs/heads/main
git -C "$p" branch sources
echo "and more" >> "$p/README.md"; commit_all "$p" "readme 2"
expect "an ordinary word and a link to the website's docs are fine in a subject" 0 "(no tag)" \
  real --update "Update sources, see https://sitboard.com/$dd/start"

# Review 2.12: an undo removes the release tag and leaves no trace; a history
# scan that errors (exit 2) undoes the commit too.
p10="$(make_private p10)"; t10="$(make_remote_target p10)"; b10="$work/bare-p10.git"
seed="$work/seed-p10"; git init -q -b main "$seed"; echo old > "$seed/OLD.md"; commit_all "$seed" "old notes for ${deny_word}"
git -C "$seed" push -q "$b10" HEAD:refs/heads/main
git -C "$t10" fetch -q "$b10" main && git -C "$t10" reset -q --hard FETCH_HEAD
head10="$(git -C "$t10" rev-parse HEAD)"
git -C "$t10" config gc.auto 1   # an auto-gc would pack the commit before the undo
expect "a release whose history scan fails is undone" 1 "failed check-private --history" \
  env PUBLISH_TEST_REMOTE="$b10" PUBLISH_IDENT="$IDENT" bash "$p10/scripts/publish.sh" "$t10" --commit
[[ -z "$(git -C "$t10" tag)" && "$(git -C "$t10" rev-parse HEAD)" == "$head10" ]] \
  && ok "the undo removed the release tag and the commit" || bad "undo left a tag or commit" "$(git -C "$t10" tag; git -C "$t10" log --oneline -2)"
! git -C "$t10" fsck --unreachable --no-reflogs 2>/dev/null | grep -qE 'commit|tag' && ! git -C "$t10" reflog 2>/dev/null | grep -qi 'sitboard' \
  && ok "the undone commit and tag are pruned and gone from the reflog" || bad "the undone commit left traces" "$(git -C "$t10" fsck --unreachable --no-reflogs 2>&1 | head -3)"
p11="$(make_private p11)"; t11="$(make_remote_target p11)"; b11="$work/bare-p11.git"
git -C "$t11" commit -q --allow-empty -m init -c user.name=x 2>/dev/null || commit_all "$t11" "first public"
git -C "$t11" push -q "$b11" HEAD:refs/heads/main
git -C "$t11" update-ref refs/check-private/pull/1/head HEAD
head11="$(git -C "$t11" rev-parse HEAD)"
expect "a history scan that errors undoes the commit" 1 "failed check-private --history" \
  env PUBLISH_TEST_REMOTE="$b11" PUBLISH_IDENT="$IDENT" bash "$p11/scripts/publish.sh" "$t11" --update "Snapshot"
[[ "$(git -C "$t11" rev-parse HEAD)" == "$head11" ]] && ok "the erroring scan left HEAD as it was" || bad "erroring scan kept the commit" ""

# Review 2.10: a release needs the grouping UI the README describes.
p12="$(make_private p12)"; t12="$(make_remote_target p12)"
read -r -a needs <<<"$(sed -n 's/^RELEASE_NEEDS=(\(.*\))$/\1/p' "$here/publish.sh")"
git -C "$p12" rm -q -- "${needs[@]}"; commit_all "$p12"
expect "a release without the grouping UI is refused" 1 "project tiles and the side rail" \
  env PUBLISH_TEST_REMOTE="$work/bare-p12.git" PUBLISH_IDENT="$IDENT" bash "$p12/scripts/publish.sh" "$t12" --commit
expect "a stage-only run without it only notes it" 0 "needs the grouping UI" rehearse "$p12/scripts/publish.sh" "$t12"
p9="$(make_private p9)"; t9="$(make_remote_target p9)"
sed -i.bak "s/^RELEASE_VERSION := .*/RELEASE_VERSION := 9.9.10/" "$p9/Makefile"; rm "$p9/Makefile.bak"; commit_all "$p9"
expect "a release needs a changelog entry for its version" 1 "no '## 9.9.10'" \
  env PUBLISH_TEST_REMOTE="$work/bare-p9.git" PUBLISH_IDENT="$IDENT" bash "$p9/scripts/publish.sh" "$t9" --commit
expect "a malformed PUBLISH_IDENT is refused" 1 "PUBLISH_IDENT" \
  env PUBLISH_TEST_REMOTE="$work/bare-p9.git" PUBLISH_IDENT="nobody" bash "$p9/scripts/publish.sh" "$t9" --update "x"

# --- package.sh and verify-release.sh -------------------------------------------

p="$(make_private pk)"
c="$(git -C "$p" rev-parse --short HEAD)"
out1="$work/rel1"
dev() { env PACKAGE_DEV=1 "$@"; }
DV="dev-$V"
expect "package builds the four archives (development build)" 0 "sitboard-$DV-linux-x86_64.tar.gz" \
  dev env SOURCE_DATE_EPOCH=1700000000 bash "$p/scripts/package.sh" "$V" "$out1"
names="$(cd "$out1" && ls | tr '\n' ' ')"
want_names=""
for s in linux-arm64 linux-x86_64 macos-arm64 macos-x86_64; do
  want_names="$want_names sitboard-$DV-$s.tar.gz sitboard-$DV-$s.tar.gz.sha256"
done
[[ " $names" == "$want_names " ]] && ok "archive and checksum names" || bad "archive names" "got:  $names
want:$want_names"
listing="$(tar -tzf "$out1/sitboard-$DV-linux-x86_64.tar.gz" | tr '\n' ' ')"
[[ "$listing" == "LICENSE README.md THIRD_PARTY_NOTICES.md sitboard " ]] && ok "archive contents: the binary, its licence and the notices" || bad "archive contents" "$listing"
(cd "$out1" && (shasum -a 256 -c ./*.sha256 >/dev/null 2>&1 || sha256sum -c ./*.sha256 >/dev/null 2>&1)) \
  && ok "checksum files verify" || bad "checksum files verify" "$(cat "$out1"/*.sha256)"
# Two clones in different folders build the same bytes.
for clone in ca cb; do
  git clone -q "$p" "$work/clone-$clone"
  mkdir -p "$work/clone-$clone/internal/server/dist"; cp "$p/internal/server/dist/index.html" "$work/clone-$clone/internal/server/dist/"
  dev env SOURCE_DATE_EPOCH=1700000000 bash "$work/clone-$clone/scripts/package.sh" "$V" "$work/rel-$clone" >/dev/null 2>&1
done
if [[ -s "$work/rel-ca/sitboard-$DV-linux-x86_64.tar.gz.sha256" ]] && cmp -s <(cd "$work/rel-ca" && cat ./*.sha256) <(cd "$work/rel-cb" && cat ./*.sha256) \
   && cmp -s <(cd "$out1" && cat ./*.sha256) <(cd "$work/rel-ca" && cat ./*.sha256); then
  ok "builds of one commit in two different folders are byte for byte the same"
else
  bad "reproducible archives" "$(cat "$work"/rel-ca/*.sha256 "$work"/rel-cb/*.sha256 2>&1)"
fi
expect "a development build says so in --version" 0 "sitboard $DV ($c)" \
  bash "$p/scripts/verify-release.sh" "$out1" "$DV" "$c"
# Archives that are named 9.9.7 but hold the 9.9.9 build: --version tells.
mkdir -p "$work/rel-renamed"
for s in macos-arm64 macos-x86_64 linux-arm64 linux-x86_64; do
  cp "$out1/sitboard-$DV-$s.tar.gz" "$work/rel-renamed/sitboard-dev-9.9.7-$s.tar.gz"
  (cd "$work/rel-renamed" && { shasum -a 256 "sitboard-dev-9.9.7-$s.tar.gz" 2>/dev/null || sha256sum "sitboard-dev-9.9.7-$s.tar.gz"; } > "sitboard-dev-9.9.7-$s.tar.gz.sha256")
done
expect "verify-release refuses archives whose binary is another version" 1 "want 'sitboard dev-9.9.7 ($c)'" \
  bash "$p/scripts/verify-release.sh" "$work/rel-renamed" "dev-9.9.7" "$c"
expect "verify-release refuses another commit" 1 "want 'sitboard $DV (0000000)'" \
  bash "$p/scripts/verify-release.sh" "$out1" "$DV" 0000000
expect "verify-release needs the commit" 1 "usage" bash "$p/scripts/verify-release.sh" "$out1" "$DV"
expect "verify-release --rebuild finds the same bytes" 0 "is byte for byte the same" \
  dev env SOURCE_DATE_EPOCH=1700000000 bash "$p/scripts/verify-release.sh" "$out1" "$DV" "$c" --rebuild
expect "REQUIRE_SIGNATURE=1 refuses unsigned archives" 1 "FAIL" \
  env REQUIRE_SIGNATURE=1 bash "$p/scripts/verify-release.sh" "$out1" "$DV" "$c"
fakeos="$work/fake-linux"; mkdir -p "$fakeos"
printf '#!/bin/sh\ncase "$1" in -s) echo Linux ;; -m) echo x86_64 ;; *) echo Linux ;; esac\n' > "$fakeos/uname"; chmod +x "$fakeos/uname"
expect "REQUIRE_SIGNATURE=1 off macOS fails, never skips" 1 "needs macOS" \
  env PATH="$fakeos:$PATH" REQUIRE_SIGNATURE=1 bash "$p/scripts/verify-release.sh" "$out1" "$DV" "$c"
expect "EXPECTED_TEAM_ID must look like a team id" 2 "EXPECTED_TEAM_ID" \
  env REQUIRE_SIGNATURE=1 EXPECTED_TEAM_ID='x;y' bash "$p/scripts/verify-release.sh" "$out1" "$DV" "$c"
cp -R "$out1" "$work/rel-bad"
printf 'x' >> "$work/rel-bad/sitboard-$DV-macos-x86_64.tar.gz"
expect "verify-release refuses a changed archive" 1 "checksum" bash "$p/scripts/verify-release.sh" "$work/rel-bad" "$DV" "$c"

# Review 6: a release build is made only in a clone of the public repository
# at the tag of its version.
expect "package refuses a release build outside the public repository" 1 "clone of the public repository" \
  bash "$p/scripts/package.sh" "$V" "$work/rel-priv"
git -C "$p" remote add origin "$PUBLIC_URL"
expect "package refuses a release build of an untagged commit" 1 "is not the tag v$V" \
  bash "$p/scripts/package.sh" "$V" "$work/rel-untagged"
git -C "$p" -c user.name=test -c user.email=test@example.com -c tag.gpgsign=false tag -a "v$V" -m "sitboard $V"
git -C "$p" tag aaa-also-here   # a second tag on HEAD must not hide v$V
expect "package builds a release at the tag in the public repository" 0 "release archives" \
  bash "$p/scripts/package.sh" "$V" "$work/rel-tagged"
[[ -s "$work/rel-tagged/sitboard-$V-linux-x86_64.tar.gz" ]] && ok "a release build has the release names" || bad "release names" "$(ls "$work/rel-tagged")"
expect "a relative output folder is the caller's" 0 "release archives" \
  bash -c 'cd "$1" && bash "$2/scripts/package.sh" "$3" rel-relative' _ "$work" "$p" "$V"
[[ -s "$work/rel-relative/sitboard-$V-linux-x86_64.tar.gz" && ! -e "$p/rel-relative" ]] \
  && ok "the relative output folder got the archives" || bad "relative output folder" "$(ls "$p")"
git -C "$p" tag -d "v$V" aaa-also-here >/dev/null; git -C "$p" remote remove origin
[[ ! -e "$work/rel-priv" && ! -e "$work/rel-untagged" ]] \
  && ok "refused release builds created nothing" || bad "refused builds created output" "$(ls -d "$work"/rel-priv "$work"/rel-untagged 2>&1)"

git -C "$p" rm -q THIRD_PARTY_NOTICES.md; commit_all "$p" "drop notices"
expect "package refuses a build without the third-party notices" 1 "THIRD_PARTY_NOTICES.md" dev bash "$p/scripts/package.sh" "$V" "$work/rel-nn"
t="$(make_target pk-nn)"
expect "publish refuses a tree without the third-party notices" 1 "not tracked: THIRD_PARTY_NOTICES.md" rehearse "$p/scripts/publish.sh" "$t"
echo '# Third-party notices' > "$p/THIRD_PARTY_NOTICES.md"; commit_all "$p" "notices back"
rm "$p/internal/server/dist/index.html"
expect "package refuses a build without the web page" 1 "web" dev bash "$p/scripts/package.sh" "$V" "$work/rel3"
echo '<!doctype html>' > "$p/internal/server/dist/index.html"
echo dirty >> "$p/README.md"
expect "package refuses a dirty tree" 1 "not clean" dev bash "$p/scripts/package.sh" "$V" "$work/rel4"
git -C "$p" checkout -q -- README.md
expect "package refuses a bad version" 1 "bad version" dev bash "$p/scripts/package.sh" "v1" "$work/rel5"
expect "package refuses NOTARY_PROFILE without SIGN_IDENTITY" 1 "NOTARY_PROFILE needs SIGN_IDENTITY" \
  dev env NOTARY_PROFILE=x bash "$p/scripts/package.sh" "$V" "$work/rel6"

# --- render-formula.sh and tap.sh -------------------------------------------------

s1="$(printf 'a%.0s' $(seq 1 64))"; s2="$(printf 'b%.0s' $(seq 1 64))"
s3="$(printf 'c%.0s' $(seq 1 64))"; s4="$(printf 'd%.0s' $(seq 1 64))"
formula="$(bash "$here/render-formula.sh" 1.2.3 "$s1" "$s2" "$s3" "$s4" 2>&1)"
host_twice "the rendered formula" "$formula"
# The address tap.sh downloads the archives from, as tap.sh builds it: the
# formula must point at the same files, and the host must be there once.
# shellcheck disable=SC2034 # VERSION is read by the evaluated DL line
tap_dl="$(VERSION=1.2.3; eval "$(sed -n -e '/^REPO=/p' -e '/^DL=/p' "$here/tap.sh")"; echo "${DL:-}")"
[[ "$tap_dl" == "https://github.com/IZZY-Agency/sitboard/releases/download/v1.2.3" ]] \
  && ok "tap.sh downloads from the release's own address" || bad "tap.sh download address" "'$tap_dl'"
f_fail=""
for needle in 'class Sitboard < Formula' 'homepage "https://sitboard.com"' 'license "MIT"' \
  'strategy :github_latest' 'bin.install "sitboard"' 'shell_output("#{bin}/sitboard --version")' \
  "url \"$tap_dl/sitboard-1.2.3-macos-arm64.tar.gz\"
      sha256 \"$s1\"" \
  "url \"$tap_dl/sitboard-1.2.3-macos-x86_64.tar.gz\"
      sha256 \"$s2\"" \
  "url \"$tap_dl/sitboard-1.2.3-linux-arm64.tar.gz\"
      sha256 \"$s3\"" \
  "url \"$tap_dl/sitboard-1.2.3-linux-x86_64.tar.gz\"
      sha256 \"$s4\""; do
  [[ "$formula" == *"$needle"* ]] || f_fail="$f_fail
missing: $needle"
done
[[ "$formula" == *"{{"* ]] && f_fail="$f_fail
a placeholder is left"
[[ -z "$f_fail" ]] && ok "the formula renders for a fake version" || bad "formula render" "$f_fail
$formula"
expect "render refuses a bad checksum" 1 "sha256" bash "$here/render-formula.sh" 1.2.3 "$s1" "$s2" "$s3" nothex
expect "render refuses a bad version" 1 "version" bash "$here/render-formula.sh" 'v1;x' "$s1" "$s2" "$s3" "$s4"

# A tap checkout for a rehearsal: its origin is a local bare repository
# standing in for the tap, and its README lists packages.
make_tap() {
  local b="$work/tap-$1.git" seed="$work/tap-seed-$1" t="$work/tap-$1"
  git init -q --bare -b main "$b"
  git init -q -b main "$seed"
  printf '# tap\n\n```sh\nbrew install izzy-agency/tap/other\n```\n\n| Package | What | Source |\n|---|---|---|\n| `other` (formula) | Other tool | x |\n' > "$seed/README.md"
  mkdir -p "$seed/Formula"; echo '# other' > "$seed/Formula/other.rb"
  commit_all "$seed" seed
  git -C "$seed" push -q "$b" HEAD:refs/heads/main
  git clone -q "$b" "$t"
  echo "$t"
}
p="$(make_private tp)"
mkdir -p "$p/bin/release"
git -C "$p" remote add origin "$PUBLIC_URL"
git -C "$p" -c user.name=test -c user.email=test@example.com -c tag.gpgsign=false tag -a "v$V" -m "sitboard $V"
bash "$p/scripts/package.sh" "$V" "$p/bin/release/$V" >/dev/null 2>&1
pub="$work/published"; cp -R "$p/bin/release/$V" "$pub"
tap_env() { env TAP_TEST_ASSETS_DIR="$pub" TAP_TEST_REMOTE="$work/tap-$1.git" TAP_NO_STYLE=1 PUBLISH_IDENT="$IDENT" "${@:2}"; }
tap="$(make_tap t1)"
expect "tap writes and commits the formula" 0 "REHEARSAL: no push command" tap_env t1 bash "$p/scripts/tap.sh" "$tap"
tap_fail=""
grep -q "sitboard-$V-macos-arm64.tar.gz" "$tap/Formula/sitboard.rb" || tap_fail="no archive url"
sha_pub="$(cut -d' ' -f1 "$pub/sitboard-$V-linux-arm64.tar.gz.sha256")"
grep -q "sha256 \"$sha_pub\"" "$tap/Formula/sitboard.rb" || tap_fail="$tap_fail; no published checksum"
grep -q 'brew install izzy-agency/tap/sitboard' "$tap/README.md" || tap_fail="$tap_fail; no install line"
grep -q '`sitboard` (formula)' "$tap/README.md" || tap_fail="$tap_fail; no table row"
[[ "$(git -C "$tap" log -1 --format='%s|%an <%ae>')" == "sitboard $V|$IDENT" ]] || tap_fail="$tap_fail; commit $(git -C "$tap" log -1 --format='%s|%an <%ae>')"
[[ "$LAST_OUT" != *"push --no-follow-tags"* ]] || tap_fail="$tap_fail; a rehearsal printed a push command"
[[ -z "$tap_fail" ]] && ok "the tap commit holds the formula, README lines and identity" || bad "tap result" "$tap_fail"
git -C "$tap" push -q "$work/tap-t1.git" HEAD:refs/heads/main   # the manual push, to the stand-in
expect "tap with nothing new has nothing to commit" 0 "nothing to commit" tap_env t1 bash "$p/scripts/tap.sh" "$tap"
tap2="$(make_tap t2)"
cp "$pub/sitboard-$V-linux-arm64.tar.gz" "$work/keep.tgz"
printf 'x' >> "$pub/sitboard-$V-linux-arm64.tar.gz"
expect "tap refuses a published archive that does not match its checksum file" 1 "hashes to" \
  tap_env t2 bash "$p/scripts/tap.sh" "$tap2"
cp "$work/keep.tgz" "$pub/sitboard-$V-linux-arm64.tar.gz"
cp "$p/bin/release/$V/sitboard-$V-linux-arm64.tar.gz.sha256" "$work/keep.sha256"
printf '%064d  sitboard-%s-linux-arm64.tar.gz\n' 0 "$V" > "$p/bin/release/$V/sitboard-$V-linux-arm64.tar.gz.sha256"
expect "tap refuses when the local build's checksum differs from the published one" 1 "local" \
  tap_env t2 bash "$p/scripts/tap.sh" "$tap2"
cp "$work/keep.sha256" "$p/bin/release/$V/sitboard-$V-linux-arm64.tar.gz.sha256"
[[ -z "$(git -C "$tap2" status --porcelain)" && "$(git -C "$tap2" rev-list --count HEAD)" == 1 ]] \
  && ok "a refused tap run leaves the tap untouched" || bad "tap untouched" "$(git -C "$tap2" status --short)"

# Review 7, 9: rehearsal switches never reach a real tap checkout; the
# checkout root, a relative path, and a brew style failure.
tap3="$(make_tap t3)"
git -C "$tap3" remote set-url origin "$TAP_URL"
expect "a tap rehearsal refuses a checkout whose origin is the real tap" 1 "not the tap checkout" \
  tap_env t3 bash "$p/scripts/tap.sh" "$tap3"
expect "TAP_TEST_ASSETS_DIR without TAP_TEST_REMOTE is refused" 1 "needs TAP_TEST_REMOTE" \
  env TAP_TEST_ASSETS_DIR="$pub" PUBLISH_IDENT="$IDENT" bash "$p/scripts/tap.sh" "$tap3"
[[ -z "$(git -C "$tap3" status --porcelain)" && "$(git -C "$tap3" rev-list --count HEAD)" == 1 ]] \
  && ok "the real-origin tap checkout is untouched" || bad "real tap touched" "$(git -C "$tap3" status --short)"
tap4="$(make_tap t4)"; mkdir -p "$tap4/Formula/sub"
expect "tap refuses a folder inside the checkout" 1 "not the checkout root" tap_env t4 bash "$p/scripts/tap.sh" "$tap4/Formula"
expect "a relative tap path is the caller's folder" 0 "REHEARSAL: no push command" \
  tap_env t4 bash -c 'cd "$1" && bash "$2/scripts/tap.sh" tap-t4' _ "$work" "$p"
tap5="$(make_tap t5)"
fakebrew="$work/fake-brew"; mkdir -p "$fakebrew"
printf '#!/bin/sh\necho "style: offence" >&2\nexit 1\n' > "$fakebrew/brew"; chmod +x "$fakebrew/brew"
expect "a formula brew style rejects is refused" 1 "brew style rejected" \
  env PATH="$fakebrew:$PATH" TAP_TEST_ASSETS_DIR="$pub" TAP_TEST_REMOTE="$work/tap-t5.git" PUBLISH_IDENT="$IDENT" \
  bash "$p/scripts/tap.sh" "$tap5"
[[ -z "$(git -C "$tap5" status --porcelain)" && ! -e "$tap5/Formula/sitboard.rb" ]] \
  && ok "a rejected formula leaves the tap as it was" || bad "tap left dirty" "$(git -C "$tap5" status --short)"

# Review 2.5, 2.11: no push URL, no real tap as any remote in a rehearsal,
# no TAP_NO_STYLE in a real run, and an abort after writing restores.
tap6="$(make_tap t6)"; git -C "$tap6" config remote.origin.pushurl "$TAP_URL"
expect "tap refuses a checkout with a push URL" 1 "push URL" tap_env t6 bash "$p/scripts/tap.sh" "$tap6"
tap7="$(make_tap t7)"; git -C "$tap7" remote add upstream "$TAP_URL"
expect "a tap rehearsal refuses a checkout with the real tap as any remote" 1 "a remote that is the tap" tap_env t7 bash "$p/scripts/tap.sh" "$tap7"
expect "TAP_NO_STYLE=1 in a real run is refused" 1 "rehearsals only" \
  env TAP_NO_STYLE=1 PUBLISH_IDENT="$IDENT" bash "$p/scripts/tap.sh" "$tap7"
if [[ "$(id -u)" != 0 ]]; then
  tap8="$(make_tap t8)"
  chmod 555 "$tap8"   # Formula/ stays writable: the formula is written, then the README update aborts
  out="$(tap_env t8 bash "$p/scripts/tap.sh" "$tap8" 2>&1)"; rc=$?
  host_twice "the aborted tap run" "$out"
  chmod 755 "$tap8"
  [[ "$rc" != 0 && -z "$(git -C "$tap8" status --porcelain)" && ! -e "$tap8/Formula/sitboard.rb" ]] \
    && ok "an abort after the formula is written restores the tap" || bad "abort left the tap dirty" "exit $rc: $(git -C "$tap8" status --short)
$out"
fi

# --- release.sh ----------------------------------------------------------------------

# A public repository stand-in with v$V tagged, a fake gh that records its
# calls, and a dry run: it checks, prints the plan, and builds or publishes
# nothing.
p="$(make_private rl)"
pubb="$work/release-public.git"
git init -q --bare -b main "$pubb"
git -C "$p" push -q "$pubb" HEAD:refs/heads/main
git -C "$p" -c user.name=test -c user.email=test@example.com -c tag.gpgsign=false tag -a "v$V" -m "sitboard $V"
git -C "$p" push -q "$pubb" "refs/tags/v$V"
fakebin="$work/fake-gh"; mkdir -p "$fakebin"; ghlog="$work/gh.log"; : > "$ghlog"
cat > "$fakebin/gh" <<EOF
#!/bin/sh
echo "\${GH_HOST:-}|\${GH_REPO:-}|\$*" >> "$ghlog"
case "\$1 \$2" in
  "auth status") exit 0 ;;
  "release view") [ -n "\${FAKE_RELEASE_EXISTS:-}" ] && exit 0; exit 1 ;;
esac
exit 0
EOF
chmod +x "$fakebin/gh"
rel() { env PATH="$fakebin:$PATH" RELEASE_TEST_REMOTE="$pubb" SIGN_IDENTITY="Developer ID Application: Test" NOTARY_PROFILE=test \
  bash "$p/scripts/release.sh" "$@"; }
expect "release.sh is a dry run by default" 0 "dry run: nothing was built or published" rel
r_fail=""
for needle in "make release" "make verify-release REQUIRE_SIGNATURE=1" "gh release create v$V" \
  "--repo github.com/IZZY-Agency/sitboard" "sitboard-$V-linux-x86_64.tar.gz.sha256" "First release." "scripts/tap.sh" \
  "release page: https://github.com/IZZY-Agency/sitboard/releases/tag/v$V"; do
  [[ "$LAST_OUT" == *"$needle"* ]] || r_fail="$r_fail; missing: $needle"
done
[[ "$LAST_OUT" == *Older* ]] && r_fail="$r_fail; notes hold the older entry"
grep -q 'release create' "$ghlog" && r_fail="$r_fail; gh release create was called"
[[ -e "$p/bin/release" ]] && r_fail="$r_fail; something was built"
[[ -z "$r_fail" ]] && ok "the dry run prints the plan and the notes, and runs nothing" || bad "dry run" "${r_fail#; }
$LAST_OUT"
# Issue 45: --go cannot run here (it needs real signing and refuses a
# stand-in), so its release address is checked in the script's text.
rs="$here/release.sh"
lines_of() { grep -nE -- "$1" "$rs" | cut -d: -f1 | tr '\n' ' ' | sed 's/ $//'; }
# (a) REPO holds the host: no code line puts it in again after a scheme.
twice="$(grep -nE '^[[:space:]]*[^#[:space:]].*[a-z]+://[^[:space:]"]*github\.com/\$\{?REPO' "$rs")"
[[ -z "$twice" ]] && ok "release.sh never builds an address with the host before \$REPO" || bad "release.sh adds the host to \$REPO" "$twice"
# (b) PAGE is set once, at top level, before the dry run and --go split, so
# both paths have it.
split="$(lines_of '^if \(\(GO == 0\)\); then$')"
page_all="$(lines_of '^[[:space:]]*((local|export|readonly|declare)[[:space:]]+)?PAGE=')"
page_top="$(lines_of '^PAGE=')"
[[ "$split" =~ ^[0-9]+$ && "$page_top" =~ ^[0-9]+$ && "$page_all" == "$page_top" ]] && ((page_top < split)) \
  && ok "release.sh sets PAGE once, at top level, before the dry run and --go split" \
  || bad "PAGE in release.sh" "split at '$split', PAGE set at '$page_all', at top level '$page_top'"
# (c) --go prints PAGE after the release is created, at top level with no
# exit before it.
create="$(lines_of '^gh release create ')"
released="$(lines_of '^[[:space:]]*step "released: \$\{?PAGE\}?"$')"
released_top="$(lines_of '^step "released: \$\{?PAGE\}?"$')"
exits="$(awk -v a="${create:-0}" -v b="${released_top:-0}" 'NR > a && NR < b && /^exit([[:space:]]|$)/ { print NR }' "$rs")"
[[ "$split" =~ ^[0-9]+$ && "$create" =~ ^[0-9]+$ && "$released_top" =~ ^[0-9]+$ && "$released" == "$released_top" && -z "$exits" ]] \
  && ((split < create && create < released_top)) \
  && ok "release.sh --go prints PAGE after the release is created" \
  || bad "the --go release line" "split '$split', gh release create '$create', released lines '$released' (top level '$released_top'), exits between '$exits'"
if grep -q '^||release view v'"$V"' --repo github.com/IZZY-Agency/sitboard' "$ghlog"; then
  ok "gh is called with the host in --repo and no GH_HOST or GH_REPO"
else
  bad "gh call" "$(cat "$ghlog")"
fi
: > "$ghlog"
expect "release.sh ignores GH_HOST and GH_REPO" 0 "dry run" env GH_HOST=elsewhere.example GH_REPO=someone/else \
  PATH="$fakebin:$PATH" RELEASE_TEST_REMOTE="$pubb" SIGN_IDENTITY="Developer ID Application: Test" NOTARY_PROFILE=test \
  bash "$p/scripts/release.sh"
grep -q 'elsewhere\|someone/else' "$ghlog" && bad "gh saw GH_HOST or GH_REPO" "$(cat "$ghlog")" || ok "gh never sees GH_HOST or GH_REPO"
FAKE_RELEASE_EXISTS=1 expect "release.sh refuses a release that exists" 1 "already exists" rel
git -C "$p" push -q "$pubb" ":refs/tags/v$V"
expect "release.sh refuses when the tag is not on the public repository" 1 "not on the public repository" rel
expect "release.sh refuses unknown options" 2 "usage" rel --push
# Review 3.1: the release's make sees no MAKEFLAGS, MAKEFILES or Go
# switches from the environment, and REQUIRE_SIGNATURE=1 as an argument wins.
mkdir -p "$work/mk"
printf 'x:\n\t@echo "sig=$(REQUIRE_SIGNATURE) extra=$(EXTRA) goflags=[$$GOFLAGS] goenv=[$$GOENV] gotoolchain=[$$GOTOOLCHAIN]"\n' > "$work/mk/Makefile"
printf 'EXTRA := from-makefiles\n' > "$work/mk/extra.mk"
eval "$(sed -n '/^clean_make() {/,/^}/p' "$here/release.sh")"
mk_out="$(cd "$work/mk" && MAKEFLAGS='REQUIRE_SIGNATURE=0' MAKEFILES="$work/mk/extra.mk" GOFLAGS=-overlay=/x GOTOOLCHAIN=go1.0 \
  clean_make -s x REQUIRE_SIGNATURE=1 2>&1)"
[[ "$mk_out" == "sig=1 extra= goflags=[] goenv=[off] gotoolchain=[]" ]] \
  && ok "the release's make ignores MAKEFLAGS, MAKEFILES and Go switches" || bad "clean_make" "$mk_out"
plain_out="$(cd "$work/mk" && MAKEFLAGS='REQUIRE_SIGNATURE=0' make -s x 2>&1)"
[[ "$plain_out" == sig=0* ]] && ok "(without it, MAKEFLAGS would turn the signature check off)" || bad "plain make" "$plain_out"
grep -q 'clean_make verify-release REQUIRE_SIGNATURE=1' "$here/release.sh" \
  && ok "release.sh passes REQUIRE_SIGNATURE=1 to make as an argument" || bad "release.sh verify call" ""

# Review 2.2: --go never runs with a stand-in (it stops before building).
: > "$ghlog"
expect "release.sh --go refuses RELEASE_TEST_REMOTE" 1 "never runs with RELEASE_TEST_REMOTE" rel --go
grep -q 'release create' "$ghlog" && bad "--go with a stand-in called gh release create" "" || ok "--go with a stand-in called nothing"

# --- this repository -------------------------------------------------------------

# One version: the Makefile's RELEASE_VERSION heads the changelog.
mk_version="$(sed -n 's/^RELEASE_VERSION := //p' "$repo/Makefile")"
cl_version="$(sed -n 's/^## \([0-9][0-9.]*\)$/\1/p' "$repo/CHANGELOG.md" | head -1)"
[[ -n "$mk_version" && "$mk_version" == "$cl_version" ]] \
  && ok "the Makefile version ($mk_version) heads CHANGELOG.md" \
  || bad "version source" "Makefile: '$mk_version', CHANGELOG.md: '$cl_version'"
# The public README shows the board, not a placeholder, and says its data is a sample.
if grep -q '<!-- SCREENSHOT' "$repo/README.md" || ! grep -qxF '![The sitboard board in tiles mode](https://sitboard.com/board.png)' "$repo/README.md" \
   || ! grep -qxF '*Sample data.*' "$repo/README.md"; then
  bad "the README still has the screenshot placeholder, or lacks the image and its caption" "$(grep -n -i 'screenshot\|board.png\|sample data' "$repo/README.md")"
else
  ok "the README shows the board image with its sample-data caption"
fi
# No exported text calls the link single-use (it is valid for the whole run).
once="one""[- ]time"
read -r -a allow_paths <<<"$(sed -n '/^ALLOWLIST=(/,/^)/p' "$here/publish.sh" | sed -e '1d' -e '$d' -e 's/#.*//' | tr '\n' ' ')"
exported_files="$(cd "$repo" && git ls-files -- "${allow_paths[@]}")"
said="$(cd "$repo" && printf '%s\n' "$exported_files" | while IFS= read -r f; do grep -IliE "$once" -- "$f" 2>/dev/null; done)"
[[ -z "$said" ]] && ok "no exported file calls the link single-use" || bad "exported files that call the link single-use" "$said"
# A release build cannot embed a stale notices file: make release runs the
# notices check before it packs anything.
if command -v make >/dev/null 2>&1; then
  plan="$(make -s -n -C "$repo" release 2>/dev/null)"
  before="${plan%%scripts/package.sh*}"
  [[ "$plan" == *"scripts/package.sh"* && "$before" == *"scripts/notices.sh --check"* ]] \
    && ok "make release checks the notices before it packs" || bad "make release does not check the notices first" "$plan"
fi
# The website stays out: tool workflows are exported by name, never the
# whole .github folder, and the exported ci.yml has no website job.
if grep -qx '  .github' <(sed -n '/^ALLOWLIST=(/,/^)/p' "$here/publish.sh" | tr ' ' '\n' | sed 's/^/  /') \
   || ! grep -q '\.github/workflows/ci\.yml' "$here/publish.sh"; then
  bad "the allowlist exports .github whole, or not ci.yml by name" "$(sed -n '/^ALLOWLIST=(/,/^)/p' "$here/publish.sh")"
else
  ok "the allowlist names ci.yml and not the whole .github folder"
fi
# The README's banner is the one other .github file, by exact name: the kinds
# of .github stay workflows (.yml) plus that one file, never every picture.
gh_allowed="$(sed -n '/^ALLOWLIST=(/,/^)/p' "$here/publish.sh" | sed -e 's/#.*//' | tr ' ' '\n' | grep '^\.github' | LC_ALL=C sort | tr '\n' ' ')"
gh_kinds="$(sed -n '/^KINDS=(/,/^)/p' "$here/publish.sh" | grep '"\.github[:=]' | tr -d ' "' | LC_ALL=C sort | tr '\n' ' ')"
[[ "$gh_allowed" == ".github/sitboard-banner.svg .github/workflows/ci.yml " && "$gh_kinds" == ".github:.yml .github=sitboard-banner.svg " ]] \
  && ok "of .github, only ci.yml and the README banner are exported, each by name" \
  || bad "the .github export set changed" "allowlist: $gh_allowed
kinds: $gh_kinds"
grep -qF '<img src=".github/sitboard-banner.svg"' "$repo/README.md" \
  && ok "the README shows the banner the export carries" || bad "the README does not show .github/sitboard-banner.svg" ""
grep -qE "^  ($ss|website|pages|deploy-$ss)[a-z-]*:" "$repo/.github/workflows/ci.yml" \
  && bad "ci.yml has a website job" "$(grep -nE '^  [a-z-]+:' "$repo/.github/workflows/ci.yml")" \
  || ok "ci.yml has no website job"
# Review 1: every allowlisted path is tracked in this repository.
allow="$(sed -n '/^ALLOWLIST=(/,/^)/p' "$here/publish.sh" | sed -e '1d' -e '$d' -e 's/#.*//')"
a_fail=""
for path in $allow; do
  git -C "$repo" ls-files --error-unmatch -- "$path" >/dev/null 2>&1 || a_fail="$a_fail $path"
done
[[ -n "$allow" && -z "$a_fail" ]] && ok "every allowlisted path is tracked in this repository" \
  || bad "allowlisted paths not tracked here:${a_fail:- (none parsed)}" ""
x_fail=""
for s in publish.sh package.sh notarize.sh verify-release.sh tap.sh render-formula.sh release.sh release_test.sh; do
  [[ -x "$here/$s" && "$(git -C "$repo" ls-files -s "scripts/$s" | cut -d' ' -f1)" == 100755 ]] || x_fail="$x_fail scripts/$s"
done
[[ -z "$x_fail" ]] && ok "release scripts are executable, in git too" || bad "not executable in git:$x_fail" ""

if ((failures > 0)); then
  echo "$failures case(s) failed"
  exit 1
fi
echo "all cases passed"
