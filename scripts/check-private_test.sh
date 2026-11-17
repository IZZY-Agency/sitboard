#!/usr/bin/env bash
# Tests for scripts/check-private.sh. Each case builds a throwaway repo and a
# throwaway deny-list; nothing here touches the real deny-list or any git setting.
#
# The sample strings are assembled from pieces at run time, so this file itself
# never contains a string the guard would flag.
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
check="$here/check-private.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

failures=0
case_no=0

# Samples, built so this file stays clean.
users_path="/Us""ers/someone/project/file.txt"
home_path="/ho""me/someone/project"
home_dev="/ho""me/dev/projects/alpha"
gh_token="ghp""_$(printf 'A%.0s' $(seq 1 24))"
ant_key="sk-""ant-$(printf 'b%.0s' $(seq 1 24))"
email="someone""@corp-mail.test"
ok_email_a="someone""@example.com"
ok_email_b="12345+someone""@users.noreply.github.com"
deny_word="zorblaxian"
deny_cap="Zorblaxian"

# new_repo <dir>: an empty git repo with a local-only identity for its commits.
# The identity is passed per command with -c; no git config file is written.
new_repo() {
  mkdir -p "$1"
  git -C "$1" init -q
}
commit_all() {
  git -C "$1" add -A
  git -C "$1" -c user.name=test -c user.email="$2" -c commit.gpgsign=false commit -q -m "test" >/dev/null
}

# run_case <name> <expected exit> <repo> <cfg dir> <CI value> [args...]
# RUN_ENV holds extra NAME=value pairs for the run (for example git settings
# passed as GIT_CONFIG_COUNT, so no git config file is written).
RUN_ENV=()
run_case() {
  local name="$1" want="$2" repo="$3" cfg="$4" ci="$5"
  shift 5
  case_no=$((case_no + 1))
  local out got
  out="$(cd "$repo" && env XDG_CONFIG_HOME="$cfg" CI="$ci" ${RUN_ENV[@]+"${RUN_ENV[@]}"} bash "$check" "$@" 2>&1)"
  got=$?
  if [[ "$got" != "$want" ]]; then
    echo "FAIL $name: exit $got, want $want"
    # shellcheck disable=SC2001 # indent every line of the output
    echo "$out" | sed 's/^/    /'
    failures=$((failures + 1))
  else
    echo "ok   $name"
  fi
  LAST_OUT="$out"
}

cfg_with_list="$work/cfg-with"
mkdir -p "$cfg_with_list/sitboard-dev"
printf '%s\n' "$deny_word" > "$cfg_with_list/sitboard-dev/denylist"
cfg_empty="$work/cfg-empty"
mkdir -p "$cfg_empty"

# A tree with one file holding <content>.
tree_with() {
  local dir="$work/repo-$case_no-$RANDOM"
  new_repo "$dir"
  printf '%s\n' "$1" > "$dir/file.txt"
  echo "$dir"
}

# Generic patterns, each must fail.
run_case "users path"         1 "$(tree_with "path $users_path")" "$cfg_with_list" ""
run_case "home path"          1 "$(tree_with "path $home_path")" "$cfg_with_list" ""
run_case "home/dev beside a real home path" 1 "$(tree_with "$home_dev and $home_path")" "$cfg_with_list" ""
run_case "github token"       1 "$(tree_with "token $gh_token")" "$cfg_with_list" ""
run_case "anthropic key"      1 "$(tree_with "key $ant_key")" "$cfg_with_list" ""
run_case "email"              1 "$(tree_with "mail $email")" "$cfg_with_list" ""
run_case "deny-list word"     1 "$(tree_with "about $deny_word here")" "$cfg_with_list" ""
run_case "deny-list word, any case" 1 "$(tree_with "about Zorblaxian here")" "$cfg_with_list" ""

# The report names the file and rule but never echoes the matched text.
if [[ "$LAST_OUT" == *orblaxian* ]]; then
  echo "FAIL report echoes the matched deny-list word"
  failures=$((failures + 1))
else
  echo "ok   report does not echo the match"
fi

# Allowed forms.
run_case "home/dev allowed"   0 "$(tree_with "path $home_dev")" "$cfg_with_list" ""
run_case "example.com and noreply allowed" 0 "$(tree_with "$ok_email_a $ok_email_b")" "$cfg_with_list" ""

# 10. Deny-list words match as case-insensitive substrings: inside camelCase,
# snake_case or a longer word they are still caught.
run_case "deny word inside camelCase"  1 "$(tree_with "const my${deny_cap}Client = 1")" "$cfg_with_list" ""
run_case "deny word inside snake_case" 1 "$(tree_with "path_${deny_word}_dir")" "$cfg_with_list" ""
run_case "deny word inside a longer word" 1 "$(tree_with "${deny_word}ness")" "$cfg_with_list" ""

# Untracked but not ignored files are scanned too.
dir="$(tree_with "clean")"
printf '%s\n' "$gh_token" > "$dir/new.txt"
run_case "untracked file scanned" 1 "$dir" "$cfg_with_list" ""

# Deny-list presence.
clean="$(tree_with "nothing to see")"
run_case "clean tree with deny-list" 0 "$clean" "$cfg_with_list" ""
run_case "missing deny-list outside CI" 1 "$clean" "$cfg_empty" ""
if [[ "$LAST_OUT" != *"deny-list missing, run scripts/make-denylist.sh"* ]]; then
  echo "FAIL missing deny-list message: got: $LAST_OUT"
  failures=$((failures + 1))
else
  echo "ok   missing deny-list message"
fi
# G14. CI=true alone never drops the deny-list; only --no-denylist does.
run_case "CI=true without deny-list still fails" 1 "$clean" "$cfg_empty" "true"
run_case "--no-denylist without deny-list, clean" 0 "$clean" "$cfg_empty" "" --no-denylist
if [[ "$LAST_OUT" != *"deny-list skipped (--no-denylist)"* ]]; then
  echo "FAIL --no-denylist note: got: $LAST_OUT"
  failures=$((failures + 1))
else
  echo "ok   --no-denylist note"
fi

# 11. An empty deny-list (or one with only blank lines) is as bad as a missing one.
cfg_blank="$work/cfg-blank"
mkdir -p "$cfg_blank/sitboard-dev"
printf '\n  \n' > "$cfg_blank/sitboard-dev/denylist"
run_case "empty deny-list outside CI" 1 "$clean" "$cfg_blank" ""
if [[ "$LAST_OUT" != *"deny-list is empty"* || "$LAST_OUT" == *clean* ]]; then
  echo "FAIL empty deny-list message: got: $LAST_OUT"
  failures=$((failures + 1))
else
  echo "ok   empty deny-list message"
fi
run_case "empty deny-list with CI=true still fails" 1 "$clean" "$cfg_blank" "true"
run_case "empty deny-list with --no-denylist" 0 "$clean" "$cfg_blank" "" --no-denylist
run_case "--no-denylist: generic rules still run" 1 "$(tree_with "$gh_token")" "$cfg_empty" "" --no-denylist
run_case "--no-denylist: history" 0 "$clean" "$cfg_empty" "" --history --no-denylist

# --history checks patch content only.
hist="$work/hist-author"
new_repo "$hist"
printf 'clean\n' > "$hist/a.txt"
commit_all "$hist" "$email"
run_case "history: author address is ignored" 0 "$hist" "$cfg_with_list" "" --history

hist2="$work/hist-diff"
new_repo "$hist2"
printf 'mail %s\n' "$email" > "$hist2/a.txt"
commit_all "$hist2" "$ok_email_a"
printf 'clean\n' > "$hist2/a.txt"
commit_all "$hist2" "$ok_email_a"
run_case "history: address in an old diff is caught" 1 "$hist2" "$cfg_with_list" "" --history

# --text <file> checks one file.
txt_bad="$work/body-bad.md"
printf 'see %s\n' "$users_path" > "$txt_bad"
txt_ok="$work/body-ok.md"
printf 'nothing private\n' > "$txt_ok"
run_case "--text bad file" 1 "$clean" "$cfg_with_list" "" --text "$txt_bad"
run_case "--text clean file" 0 "$clean" "$cfg_with_list" "" --text "$txt_ok"
run_case "--text missing deny-list" 1 "$clean" "$cfg_empty" "" --text "$txt_ok"

# 12. When git or grep fails, the guard fails closed: never "clean".
not_repo="$work/not-a-repo"
mkdir -p "$not_repo"
printf 'clean\n' > "$not_repo/a.txt"
run_case "outside a repo fails closed" 2 "$not_repo" "$cfg_with_list" ""
[[ "$LAST_OUT" == *clean* ]] && { echo "FAIL outside a repo still says clean"; failures=$((failures + 1)); }
run_case "history outside a repo fails closed" 2 "$not_repo" "$cfg_with_list" "" --history
[[ "$LAST_OUT" == *clean* ]] && { echo "FAIL history outside a repo still says clean"; failures=$((failures + 1)); }
if [[ "$(id -u)" != 0 ]]; then
  dir="$(tree_with "clean")"
  printf 'clean\n' > "$dir/locked.txt"
  chmod 000 "$dir/locked.txt"
  run_case "unreadable file fails closed (grep exit 2)" 2 "$dir" "$cfg_with_list" ""
  [[ "$LAST_OUT" == *clean* ]] && { echo "FAIL unreadable file still says clean"; failures=$((failures + 1)); }
  chmod 600 "$dir/locked.txt"
  txt_locked="$work/body-locked.md"
  printf 'clean\n' > "$txt_locked"
  chmod 000 "$txt_locked"
  run_case "--text unreadable file fails closed" 2 "$clean" "$cfg_with_list" "" --text "$txt_locked"
  chmod 600 "$txt_locked"
else
  echo "skip unreadable-file cases (running as root)"
fi

# 13 and 15. File names are scanned, and a private file name is never printed:
# a placeholder stands in for it, in its own finding and in content findings.
dir="$(tree_with "clean")"
mkdir -p "$dir/notes"
printf 'clean\n' > "$dir/notes/${deny_word}-plan.md"
run_case "deny word in a tracked file name" 1 "$dir" "$cfg_with_list" ""
if [[ "$LAST_OUT" == *orblaxian* || "$LAST_OUT" != *"[private path #"* || "$LAST_OUT" != *"file name"* ]]; then
  echo "FAIL file-name finding: got: $LAST_OUT"
  failures=$((failures + 1))
else
  echo "ok   file-name finding uses a placeholder"
fi
dir="$(tree_with "clean")"
printf 'token %s\n' "$gh_token" > "$dir/My${deny_cap}.txt"
commit_all "$dir" "$ok_email_a"
printf 'clean\n' > "$dir/untracked-${deny_word}.txt"
run_case "private file names, tracked and untracked, with content" 1 "$dir" "$cfg_with_list" ""
if [[ "$LAST_OUT" == *orblaxian* ]]; then
  echo "FAIL a private file name was printed: $LAST_OUT"
  failures=$((failures + 1))
elif [[ "$(grep -c 'file name' <<<"$LAST_OUT")" != 2 || "$LAST_OUT" != *"GitHub token"* ]]; then
  echo "FAIL want two file-name findings and a token finding: $LAST_OUT"
  failures=$((failures + 1))
else
  echo "ok   private file names hidden in every finding"
fi
dir="$(tree_with "clean")"
printf 'token %s\n' "$gh_token" > "$dir/plain-name.txt"
run_case "a clean file name is still printed" 1 "$dir" "$cfg_with_list" ""
if [[ "$LAST_OUT" != *"plain-name.txt:1: GitHub token"* ]]; then
  echo "FAIL clean file name not printed: $LAST_OUT"
  failures=$((failures + 1))
else
  echo "ok   clean file name printed"
fi

# 14. --history scans commit messages as well as patch content.
hist3="$work/hist-message"
new_repo "$hist3"
printf 'clean\n' > "$hist3/a.txt"
git -C "$hist3" add -A
git -C "$hist3" -c user.name=test -c user.email="$ok_email_a" -c commit.gpgsign=false \
  commit -q -m "add the ${deny_cap} notes" >/dev/null
run_case "history: deny word in a commit message is caught" 1 "$hist3" "$cfg_with_list" "" --history
hist4="$work/hist-trailer"
new_repo "$hist4"
printf 'clean\n' > "$hist4/a.txt"
git -C "$hist4" add -A
git -C "$hist4" -c user.name=test -c user.email="$ok_email_a" -c commit.gpgsign=false \
  commit -q -m "subject" -m "Co-Authored-By: Bot <noreply""@anthropic.com>" >/dev/null
run_case "history: a co-author noreply trailer is allowed" 0 "$hist4" "$cfg_with_list" "" --history

# hidden <name> <pattern>: fail the case when the last output contains pattern.
hidden() {
  if [[ "$LAST_OUT" == *"$2"* ]]; then
    echo "FAIL $1: the output shows text it must hide"
    failures=$((failures + 1))
  else
    echo "ok   $1"
  fi
}

# R1. A file name cannot pass options to grep.
dir="$(tree_with "token $gh_token")"
printf 'clean\n' > "$dir/--exclude=*"
printf 'clean\n' > "$dir/-v"
printf 'clean\n' > "$dir/--files-without-match"
run_case "file names that look like grep options" 1 "$dir" "$cfg_with_list" ""
if [[ "$LAST_OUT" != *"file.txt:1: GitHub token"* ]]; then
  echo "FAIL option-like file names changed the scan: $LAST_OUT"
  failures=$((failures + 1))
else
  echo "ok   option-like file names are plain files"
fi
dir="$(tree_with "clean")"
printf 'clean\n' > "$dir/-v"
run_case "a file named -v does not invert a clean scan" 0 "$dir" "$cfg_with_list" ""
txt_dash="$work/-v"
printf 'token %s\n' "$gh_token" > "$txt_dash"
run_case "--text with a file named -v" 1 "$clean" "$cfg_with_list" "" --text "$txt_dash"

# R2. A name with a newline or another control character is private: its
# content is scanned under a placeholder, and the raw name is never printed.
dir="$(tree_with "clean")"
printf 'token %s\n' "$gh_token" > "$dir/two"$'\n'"line-${deny_word}.txt"
printf 'token %s\n' "$gh_token" > "$dir/tab"$'\t'"${deny_word}.txt"
printf 'token %s\n' "$gh_token" > "$dir/ctl"$'\001'"name.txt"
run_case "names with control characters" 1 "$dir" "$cfg_with_list" ""
hidden "control-character names are never printed" "orblaxian"
hidden "control-character name without a deny word is not printed" "ctl"
if [[ "$(grep -c 'GitHub token' <<<"$LAST_OUT")" != 3 ]]; then
  echo "FAIL want three token findings under placeholders: $LAST_OUT"
  failures=$((failures + 1))
else
  echo "ok   content of control-character names is scanned"
fi

# R3. A file that looks binary is still scanned (tree and --text).
dir="$(tree_with "clean")"
printf 'about %s\000here\n' "$deny_word" > "$dir/nul.bin"
run_case "deny word in a file with a NUL byte" 1 "$dir" "$cfg_with_list" ""
dir="$(tree_with "clean")"
printf 'caf\351 %s\n' "$deny_word" > "$dir/latin1.txt"
run_case "deny word in a Latin-1 file" 1 "$dir" "$cfg_with_list" ""
txt_bin="$work/body-bin.md"
printf 'caf\351 \000 %s\n' "$deny_word" > "$txt_bin"
run_case "--text deny word in a binary-looking file" 1 "$clean" "$cfg_with_list" "" --text "$txt_bin"

# R4. A committed symlink stores its target path; that path is scanned and
# never printed. The target's own content is not the repo's and is not read.
dir="$(tree_with "clean")"
ln -s "../${deny_word}/notes.md" "$dir/link-a"
ln -s "$users_path" "$dir/link-b"
commit_all "$dir" "$ok_email_a"
run_case "symlink targets are scanned" 1 "$dir" "$cfg_with_list" ""
hidden "symlink target is never printed" "orblaxian"
if [[ "$LAST_OUT" != *"link target"*"deny-list word"* || "$LAST_OUT" != *"home path (/Users/...)"* ]]; then
  echo "FAIL want a deny-list and a home-path finding on link targets: $LAST_OUT"
  failures=$((failures + 1))
else
  echo "ok   link target findings"
fi
dir="$(tree_with "clean")"
printf 'token %s\n' "$gh_token" > "$work/outside-target.txt"
# A relative target, so the stored path holds no temp-folder path (which can sit
# under a home folder).
ln -s "../outside-target.txt" "$dir/link-out"
run_case "a symlink's target content is not read" 0 "$dir" "$cfg_with_list" ""

# R5. Running from a subfolder still scans the whole repo.
dir="$(tree_with "token $gh_token")"
mkdir -p "$dir/sub"
printf 'clean\n' > "$dir/sub/a.txt"
run_case "run from a subfolder scans the whole tree" 1 "$dir/sub" "$cfg_with_list" ""
run_case "run from a subfolder, history" 1 "$hist2" "$cfg_with_list" "" --history
mkdir -p "$hist2/sub"
run_case "run from a history subfolder" 1 "$hist2/sub" "$cfg_with_list" "" --history

# R6. --history scans annotated tag messages.
hist5="$work/hist-tag"
new_repo "$hist5"
printf 'clean\n' > "$hist5/a.txt"
commit_all "$hist5" "$ok_email_a"
git -C "$hist5" -c user.name=test -c user.email="$ok_email_a" -c tag.gpgsign=false \
  tag -a v0.0.1 -m "release for ${deny_cap}" >/dev/null
run_case "history: deny word in an annotated tag message" 1 "$hist5" "$cfg_with_list" "" --history

# R7. make-denylist.sh: tokens of 6+ letters, public names skipped, hand-added
# words kept, and a reminder to review the list. A throwaway home and config;
# PATH without agtermctl so no live session names are read.
dl_home="$work/dl-home"
dl_cfg="$work/dl-cfg"
mkdir -p "$dl_home/.claude/projects/-x-quorvant-abcde-sitboard-agterm-homebrew-github-claude-codex"
mkdir -p "$dl_cfg/sitboard-dev"
printf 'hand\n' > "$dl_cfg/sitboard-dev/denylist"
dl_out="$(HOME="$dl_home" XDG_CONFIG_HOME="$dl_cfg" PATH="/usr/bin:/bin" bash "$here/make-denylist.sh" 2>&1)"
dl_rc=$?
dl_list="$dl_cfg/sitboard-dev/denylist"
dl_fail=""
((dl_rc == 0)) || dl_fail="exit $dl_rc"
grep -qx quorvant "$dl_list" || dl_fail="$dl_fail; 6+ letter token missing"
grep -qx abcde "$dl_list" && dl_fail="$dl_fail; 5-letter token kept"
grep -qxE 'sitboard|agterm|homebrew|github|claude|codex' "$dl_list" && dl_fail="$dl_fail; public name kept"
grep -qx hand "$dl_list" || dl_fail="$dl_fail; hand-added word dropped"
[[ "$dl_out" == *"review"* ]] || dl_fail="$dl_fail; no review reminder"
[[ "$dl_out" == *quorvant* ]] && dl_fail="$dl_fail; printed a word"
if [[ -n "$dl_fail" ]]; then
  echo "FAIL make-denylist: ${dl_fail#; }"
  failures=$((failures + 1))
else
  echo "ok   make-denylist: threshold, public names, hand-added words, reminder"
fi

# S5. make-denylist.sh: ordinary words skipped, short existing words counted,
# --rebuild re-filters them, an unreadable list stops the run.
mkdir -p "$dl_home/.claude/projects/-x-content-staging-migration-dashboard-landing"
dl_out="$(HOME="$dl_home" XDG_CONFIG_HOME="$dl_cfg" PATH="/usr/bin:/bin" bash "$here/make-denylist.sh" 2>&1)"
dl_fail=""
grep -qxE 'content|staging|migration|dashboard|landing' "$dl_list" && dl_fail="ordinary word kept"
[[ "$dl_out" == *"1 existing word(s) under 6 letters"*"--rebuild"* ]] || dl_fail="$dl_fail; no short-word count"
dl_out="$(HOME="$dl_home" XDG_CONFIG_HOME="$dl_cfg" PATH="/usr/bin:/bin" bash "$here/make-denylist.sh" --rebuild 2>&1)"
grep -qx hand "$dl_list" && dl_fail="$dl_fail; --rebuild kept a short word"
grep -qx quorvant "$dl_list" || dl_fail="$dl_fail; --rebuild dropped a long word"
if [[ "$(id -u)" != 0 ]]; then
  printf 'hand\nquorvant\n' > "$dl_list"
  chmod 000 "$dl_list"
  HOME="$dl_home" XDG_CONFIG_HOME="$dl_cfg" PATH="/usr/bin:/bin" bash "$here/make-denylist.sh" >/dev/null 2>&1
  dl_rc=$?
  chmod 600 "$dl_list"
  ((dl_rc != 0)) || dl_fail="$dl_fail; unreadable list did not stop the run"
  [[ "$(cat "$dl_list")" == $'hand\nquorvant' ]] || dl_fail="$dl_fail; unreadable list was replaced"
fi
if [[ -n "$dl_fail" ]]; then
  echo "FAIL make-denylist: ${dl_fail#; }"
  failures=$((failures + 1))
else
  echo "ok   make-denylist: ordinary words, short-word count, --rebuild, unreadable list"
fi

# S1. History reads binary-looking content and files under a -diff attribute.
hist6="$work/hist-binary"
new_repo "$hist6"
printf 'x\000 %s\n' "$deny_word" > "$hist6/blob.bin"
commit_all "$hist6" "$ok_email_a"
git -C "$hist6" rm -q blob.bin
commit_all "$hist6" "$ok_email_a"
run_case "history: deny word in a removed binary-looking file" 1 "$hist6" "$cfg_with_list" "" --history
hist7="$work/hist-nodiff"
new_repo "$hist7"
printf '*.dat -diff\n' > "$hist7/.gitattributes"
printf 'about %s\n' "$deny_word" > "$hist7/notes.dat"
commit_all "$hist7" "$ok_email_a"
run_case "history: deny word in a -diff file" 1 "$hist7" "$cfg_with_list" "" --history

# S2. Tree mode scans what is staged, not only the working copy.
dir="$(tree_with "clean")"
printf 'about %s\n' "$deny_word" > "$dir/s.txt"
git -C "$dir" add s.txt
printf 'clean\n' > "$dir/s.txt"
run_case "staged content differs from the working copy" 1 "$dir" "$cfg_with_list" ""
if [[ "$LAST_OUT" != *"staged:"*"deny-list word"* ]]; then
  echo "FAIL want a staged finding: $LAST_OUT"
  failures=$((failures + 1))
else
  echo "ok   staged finding"
fi
dir="$(tree_with "about $deny_word")"
commit_all "$dir" "$ok_email_a"
printf 'clean\n' > "$dir/file.txt"
git -C "$dir" add file.txt
run_case "staging the removal of a word is clean" 0 "$dir" "$cfg_with_list" ""

# S3. History scans branch and tag names.
hist8="$work/hist-refs"
new_repo "$hist8"
printf 'clean\n' > "$hist8/a.txt"
commit_all "$hist8" "$ok_email_a"
git -C "$hist8" branch "feature/${deny_word}"
run_case "history: deny word in a branch name" 1 "$hist8" "$cfg_with_list" "" --history
git -C "$hist8" branch -D -q "feature/${deny_word}"
git -C "$hist8" tag "v1-${deny_word}"
run_case "history: deny word in a lightweight tag name" 1 "$hist8" "$cfg_with_list" "" --history

# S4. Generic patterns: a home path without a trailing slash, a fine-grained token.
home_bare="/Us""ers/someone"
run_case "home path without a trailing slash" 1 "$(tree_with "HOME=$home_bare")" "$cfg_with_list" ""
run_case "home path without a trailing slash, /home form" 1 "$(tree_with "HOME=/ho""me/someone")" "$cfg_with_list" ""
run_case "home/dev alone allowed" 0 "$(tree_with "HOME=/ho""me/dev")" "$cfg_with_list" ""
run_case "a longer name after /home/dev is a real home" 1 "$(tree_with "HOME=/ho""me/devops")" "$cfg_with_list" ""
run_case "a rule label like (/Users/...) is not a home" 0 "$(tree_with "home path (/Us""ers/...)")" "$cfg_with_list" ""
pat_token="github""_pat_$(printf 'C%.0s' $(seq 1 30))"
run_case "fine-grained GitHub token" 1 "$(tree_with "token $pat_token")" "$cfg_with_list" ""
run_case "--no-denylist: fine-grained GitHub token" 1 "$(tree_with "token $pat_token")" "$cfg_empty" "" --no-denylist

# S6. A deny word with a colon would leak through the report: refused.
cfg_colon="$work/cfg-colon"
mkdir -p "$cfg_colon/sitboard-dev"
printf '%s\nab:cd\n' "$deny_word" > "$cfg_colon/sitboard-dev/denylist"
run_case "deny word with a colon is refused" 2 "$clean" "$cfg_colon" ""
if [[ "$LAST_OUT" != *"':'"* || "$LAST_OUT" == *"ab:cd"* ]]; then
  echo "FAIL colon message: $LAST_OUT"
  failures=$((failures + 1))
else
  echo "ok   colon message names the rule, not the word"
fi

# S7. Invisible and bidirectional control characters, generated from their
# UTF-8 bytes at run time, are a generic rule (they run in CI too).
uni_rule="invisible or bidirectional control character"
for bytes in \
  '\xe2\x80\x8b' '\xe2\x80\x8c' '\xe2\x80\x8d' '\xe2\x80\x8e' '\xe2\x80\x8f' \
  '\xe2\x80\xaa' '\xe2\x80\xab' '\xe2\x80\xac' '\xe2\x80\xad' '\xe2\x80\xae' \
  '\xe2\x81\xa0' '\xe2\x81\xa6' '\xe2\x81\xa7' '\xe2\x81\xa8' '\xe2\x81\xa9' \
  '\xef\xbb\xbf' '\xd8\x9c' '\xf3\xa0\x80\x80' '\xf3\xa0\x80\x81' '\xf3\xa0\x80\xa0' \
  '\xf3\xa0\x81\xbf'; do
  dir="$(tree_with "clean")"
  # shellcheck disable=SC2059 # the format carries the byte escapes
  printf "line\nbefore${bytes}after\n" > "$dir/b.txt"
  run_case "unicode bytes $bytes" 1 "$dir" "$cfg_empty" "" --no-denylist
  if [[ "$LAST_OUT" != *"b.txt:2: $uni_rule"* || "$LAST_OUT" == *before* || "$LAST_OUT" == *after* ]]; then
    echo "FAIL unicode bytes $bytes: want file and line only: $LAST_OUT"
    failures=$((failures + 1))
  fi
done
dir="$(tree_with "clean")"
printf '\xef\xbb\xbfpackage x\n' > "$dir/bom.go"
run_case "unicode: leading byte-order mark" 1 "$dir" "$cfg_with_list" ""
dir="$(tree_with "clean")"
printf 'caf\xc3\xa9 \xe2\x80\x94 \xe2\x86\x92 \xf0\x9f\x99\x82 \xe2\x80\x8a \xe2\x80\x90 \xe2\x80\xaf \xe2\x81\xa1 \xd8\x9b \xf3\xa0\x82\x80 \xf3\x9f\xbf\xbf\n' > "$dir/ok.txt"
run_case "unicode: ordinary UTF-8 and neighbouring code points are fine" 0 "$dir" "$cfg_with_list" ""
dir="$(tree_with "clean")"
printf 'x\000y\xe2\x80\xaez\n' > "$dir/nul.txt"
run_case "unicode: a text file with a NUL byte is checked" 1 "$dir" "$cfg_with_list" ""
dir="$(tree_with "clean")"
printf 'x\000y\xe2\x80\xaez\n' > "$dir/font.woff2"
run_case "unicode: a binary asset is not checked" 0 "$dir" "$cfg_with_list" ""
dir="$(tree_with "clean")"
printf 'clean\n' > "$dir/name"$'\xe2\x80\xae'"txt.go"
printf 'x\xe2\x80\xaey\n' > "$dir/two"$'\n'"lines.txt"
run_case "unicode: in file names, hidden behind placeholders" 1 "$dir" "$cfg_with_list" ""
if [[ "$LAST_OUT" != *"$uni_rule in a file name"* || "$LAST_OUT" == *lines* || "$LAST_OUT" == *txt.go* ]]; then
  echo "FAIL unicode file names: $LAST_OUT"
  failures=$((failures + 1))
else
  echo "ok   unicode file names hidden"
fi


# --- Second review of the guard (G1 to G15) --------------------------------

# history_repo <name>: a new repo with one clean commit; prints its path.
history_repo() {
  local d="$work/$1"
  new_repo "$d"
  printf 'clean\n' > "$d/a.txt"
  commit_all "$d" "$ok_email_a"
  echo "$d"
}
# commit_msg <repo> <message>: commit everything with that message.
commit_msg() {
  git -C "$1" add -A
  git -C "$1" -c user.name=test -c user.email="$ok_email_a" -c commit.gpgsign=false \
    commit -q --allow-empty -m "$2" >/dev/null
}
# want_out <name> <pattern>: fail the case unless the last output contains pattern.
want_out() {
  if [[ "$LAST_OUT" == *"$2"* ]]; then
    echo "ok   $1"
  else
    echo "FAIL $1: $LAST_OUT"
    failures=$((failures + 1))
  fi
}

# G1. make private runs the history scan too, with the deny-list.
if command -v make >/dev/null 2>&1; then
  make_out="$(make -s -n -C "$here/.." private 2>&1)"
  if [[ "$make_out" == *"check-private.sh --history"* ]]; then
    echo "ok   make private runs --history"
  else
    echo "FAIL make private does not run --history: $make_out"
    failures=$((failures + 1))
  fi
else
  echo "skip make private case (no make)"
fi

# G2. --pull-refs adds pull request heads that no local ref reaches.
origin="$work/g2-origin.git"
git init -q --bare "$origin"
g2src="$(history_repo g2-src)"
git -C "$g2src" push -q "$origin" HEAD:refs/heads/main 2>/dev/null
printf 'about %s\n' "$deny_word" > "$g2src/pr.txt"
commit_msg "$g2src" "pull request work"
git -C "$g2src" push -q "$origin" HEAD:refs/pull/1/head 2>/dev/null
g2="$work/g2-clone"
git clone -q "file://$origin" "$g2" 2>/dev/null
run_case "history: a pull request head no ref reaches is not scanned" 0 "$g2" "$cfg_with_list" "" --history
run_case "history --pull-refs: pull request heads are scanned" 1 "$g2" "$cfg_with_list" "" --history --pull-refs
if [[ -n "$(git -C "$g2" for-each-ref refs/check-private)" ]]; then
  echo "FAIL --pull-refs left its refs behind"
  failures=$((failures + 1))
else
  echo "ok   --pull-refs removes its refs"
fi
want_out "--pull-refs says how many heads it fetched" "fetched 1 pull request head"
run_case "--pull-refs without --history is refused" 2 "$g2" "$cfg_with_list" "" --pull-refs
# H7. An origin without pull request refs gets a warning.
g2e="$work/g2-empty-clone"
git clone -q "file://$hist2" "$g2e" 2>/dev/null
# (hist2's history holds an address, so the scan itself finds one.)
run_case "--pull-refs with no pull request refs" 1 "$g2e" "$cfg_with_list" "" --history --pull-refs
want_out "--pull-refs warns when it fetched none" "warning: origin has no refs/pull"
# H6. Refs left under the namespace stop the run; a failed cleanup exits 2.
g2l="$work/g2-left"
git clone -q "file://$origin" "$g2l" 2>/dev/null
git -C "$g2l" update-ref refs/check-private/pull/9/head "$(git -C "$g2l" rev-parse origin/main)"
run_case "history refuses to run over leftover pull refs" 2 "$g2l" "$cfg_with_list" "" --history
want_out "leftover refs message names the fix" "update-ref --stdin"
git -C "$g2l" update-ref -d refs/check-private/pull/9/head
hooks="$work/g2-hooks"
mkdir -p "$hooks"
# shellcheck disable=SC2016 # the hook's own $1 and $new, not this shell's
printf '#!/bin/sh\n# Refuse every ref deletion.\n[ "$1" = prepared ] || exit 0\nwhile read -r old new ref; do\n  case "$new" in *[!0]*) ;; *) exit 1 ;; esac\ndone\n' \
  > "$hooks/reference-transaction"
chmod +x "$hooks/reference-transaction"
RUN_ENV=(GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.hooksPath "GIT_CONFIG_VALUE_0=$hooks")
run_case "a failed pull-ref cleanup exits 2" 2 "$g2l" "$cfg_with_list" "" --history --pull-refs
RUN_ENV=()
run_case "--pull-refs without an origin fails closed" 2 "$(history_repo g2-no-origin)" "$cfg_with_list" "" --history --pull-refs

# G3. Anthropic keys hold "_" too.
ant_key_u="sk-""ant-api03-$(printf 'b%.0s' $(seq 1 5))_$(printf 'c%.0s' $(seq 1 20))"
run_case "anthropic key with an underscore" 1 "$(tree_with "key $ant_key_u")" "$cfg_with_list" ""

# G4. Merge commits: content that only a merge commit adds is scanned.
g4="$(history_repo g4)"
git -C "$g4" switch -q -c side
printf 'side\n' > "$g4/side.txt"
commit_msg "$g4" "side"
git -C "$g4" switch -q -
git -C "$g4" -c user.name=test -c user.email="$ok_email_a" merge -q --no-ff --no-commit side >/dev/null 2>&1
printf 'about %s\n' "$deny_word" > "$g4/merge-only.txt"
commit_msg "$g4" "merge side"
run_case "history: content added only by a merge commit" 1 "$g4" "$cfg_with_list" "" --history

# G5. Personal git settings do not blind the scan.
RUN_ENV=(GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=color.diff GIT_CONFIG_VALUE_0=always)
dir="$(tree_with "clean")"
printf 'about %s\n' "$deny_word" > "$dir/s.txt"
git -C "$dir" add s.txt
printf 'clean\n' > "$dir/s.txt"
run_case "staged scan with color.diff=always" 1 "$dir" "$cfg_with_list" ""
RUN_ENV=()
g5r="$(history_repo g5-replace)"
commit_msg "$g5r" "notes for ${deny_cap}"
orig="$(git -C "$g5r" rev-parse HEAD)"
fake="$(git -C "$g5r" -c user.name=test -c user.email="$ok_email_a" commit-tree -p HEAD~1 -m clean "HEAD^{tree}")"
git -C "$g5r" replace "$orig" "$fake"
run_case "history with a replace ref hiding a commit" 1 "$g5r" "$cfg_with_list" "" --history
# H3. A grafts file cannot cut history off either.
g5g="$work/g5-grafts"
new_repo "$g5g"
printf 'about %s\n' "$deny_word" > "$g5g/a.txt"
commit_all "$g5g" "$ok_email_a"
printf 'clean\n' > "$g5g/a.txt"
commit_all "$g5g" "$ok_email_a"
git -C "$g5g" rev-parse HEAD > "$g5g/.git/info/grafts"
run_case "history with a grafts file hiding a commit" 1 "$g5g" "$cfg_with_list" "" --history
hidden "no grafts hint from the guard's own git calls" "hint:"

# G6. A shallow clone cannot be checked.
g6="$work/g6-shallow"
git clone -q --depth 1 "file://$hist2" "$g6" 2>/dev/null
run_case "history in a shallow clone fails closed" 2 "$g6" "$cfg_with_list" "" --history
want_out "shallow clone message" "shallow"

# G7. A tag can point at a blob or a tree.
g7="$(history_repo g7-blob)"
blob="$(printf 'about %s\n' "$deny_word" | git -C "$g7" hash-object -w --stdin)"
git -C "$g7" tag tagged-blob "$blob"
run_case "history: a tag pointing at a blob" 1 "$g7" "$cfg_with_list" "" --history
g7t="$(history_repo g7-tree)"
printf 'about %s\n' "$deny_word" > "$g7t/b.txt"
git -C "$g7t" add b.txt
tree="$(git -C "$g7t" write-tree)"
git -C "$g7t" reset -q
rm "$g7t/b.txt"
git -C "$g7t" tag tagged-tree "$tree"
run_case "history: a tag pointing at a tree" 1 "$g7t" "$cfg_with_list" "" --history
g7n="$(history_repo g7-name)"
mkdir -p "$g7n/notes"
printf 'clean\n' > "$g7n/notes/${deny_word}.md"
commit_all "$g7n" "$ok_email_a"
git -C "$g7n" rm -q "notes/${deny_word}.md"
commit_all "$g7n" "$ok_email_a"
run_case "history: a deleted file's name" 1 "$g7n" "$cfg_with_list" "" --history
hidden "history findings never print the match" "orblaxian"

# G8. Home paths in other spellings.
users_seg="Us""ers"
home_seg="ho""me"
run_case "home: Claude Code project folder" 1 "$(tree_with "dir -${users_seg}-someone-project")" "$cfg_with_list" ""
run_case "home: Claude Code project folder, /home form" 1 "$(tree_with "dir -${home_seg}-someone-project")" "$cfg_with_list" ""
run_case "home: JSON-escaped" 1 "$(tree_with "{\"cwd\":\"\\/${users_seg}\\/someone\\/x\"}")" "$cfg_with_list" ""
run_case "home: JSON-escaped, /home form" 1 "$(tree_with "{\"cwd\":\"\\/${home_seg}\\/someone\"}")" "$cfg_with_list" ""
run_case "home: URL-encoded" 1 "$(tree_with "path=%2F${users_seg}%2Fsomeone%2Fx")" "$cfg_with_list" ""
run_case "home: URL-encoded, lower case" 1 "$(tree_with "path=%2f${home_seg}%2fsomeone")" "$cfg_with_list" ""
# H4. Web routes are not homes; a lower-case Windows home is.
u="us""ers"
routes="mux.HandleFunc(\"/$u/me\", h)
const p = '/$u/profile'
<a href=\"/$u/settings\">
fetch(\"/$u/me\")
see -$u-guide and data:/$u/me"
run_case "home: web routes are fine" 0 "$(tree_with "$routes")" "$cfg_with_list" ""
run_case "home: lower-case Windows with a forward slash" 1 "$(tree_with "cd c:/us""ers/someone/x")" "$cfg_with_list" ""
run_case "home: lower-case Windows, quoted" 1 "$(tree_with "dir=\"d:/us""ers/someone\"")" "$cfg_with_list" ""
run_case "home: lower-case Windows, --text" 1 "$clean" "$cfg_with_list" "" --text "$(tree_with "c:/us""ers/someone")/file.txt"
dir="$(tree_with "clean")"
printf 'clean\n' > "$dir/name-${deny_word}.txt"
printf 'c:/us''ers/someone\n' >> "$dir/name-${deny_word}.txt"
run_case "home: lower-case Windows in a private-named file" 1 "$dir" "$cfg_with_list" ""
hidden "a private file name stays hidden with a Windows home finding" "orblaxian"
run_case "home: Windows" 1 "$(tree_with "C:\\${users_seg}\\someone\\x")" "$cfg_with_list" ""
run_case "home: Windows, JSON-escaped" 1 "$(tree_with "C:\\\\${users_seg}\\\\someone")" "$cfg_with_list" ""
run_case "home: a URL path is not a home" 0 "$(tree_with "https://api.example.com/us""ers/someone")" "$cfg_with_list" ""
run_case "home: kebab-case words are not homes" 0 "$(tree_with "nav-us""ers-list nav-ho""me-link")" "$cfg_with_list" ""
run_case "home: dev placeholder in the other spellings" 0 "$(tree_with "-${home_seg}-dev-projects \\/${home_seg}\\/dev %2F${home_seg}%2Fdev")" "$cfg_with_list" ""
dir="$(tree_with "clean")"
printf 'clean\n' > "$dir/-${users_seg}-someone-notes.md"
run_case "home: Claude Code project folder in a file name" 1 "$dir" "$cfg_with_list" ""
hidden "home in a file name is not printed" "someone"

# G9. The invisible-character rule runs on commit messages.
g9="$(history_repo g9)"
commit_msg "$g9" "$(printf 'notes for zorbl\xe2\x80\x8baxian')"
run_case "history: invisible character in a commit message" 1 "$g9" "$cfg_with_list" "" --history
want_out "history invisible-character finding" "invisible or bidirectional control character"
g9a="$(history_repo g9-asset)"
printf 'x\000y\xe2\x80\xaez\n' > "$g9a/font.woff2"
commit_all "$g9a" "$ok_email_a"
run_case "history: a binary asset is not checked for invisible characters" 0 "$g9a" "$cfg_with_list" "" --history
# H1. A copy under an asset name does not exempt the same bytes under a text name.
g9c="$(history_repo g9-copy)"
printf 'if x \xe2\x80\xae y\n' > "$g9c/main.go"
cp "$g9c/main.go" "$g9c/logo.png"
commit_all "$g9c" "$ok_email_a"
git -C "$g9c" rm -q main.go logo.png
commit_all "$g9c" "$ok_email_a"
run_case "history: a text file with the same bytes as an asset is checked" 1 "$g9c" "$cfg_empty" "" --history --no-denylist

# H2. A signed tag's message copied into a merge commit (mergetag) is scanned;
# the author, committer and tagger lines are not.
g_mt="$(history_repo g-mergetag)"
mt_commit() {
  local msg="$1" who="$2" tree parent
  tree="$(git -C "$g_mt" rev-parse 'HEAD^{tree}')"
  parent="$(git -C "$g_mt" rev-parse HEAD)"
  printf 'tree %s\nparent %s\nauthor test <%s> 0 +0000\ncommitter test <%s> 0 +0000\nmergetag object %s\n type commit\n tag v1\n tagger test <%s> 0 +0000\n \n %s\n\nclean message\n' \
    "$tree" "$parent" "$who" "$who" "$parent" "$who" "$msg" \
    | git -C "$g_mt" hash-object -t commit -w --stdin
}
git -C "$g_mt" update-ref refs/heads/mt "$(mt_commit "release for ${deny_cap}" "$ok_email_a")"
run_case "history: a mergetag message is scanned" 1 "$g_mt" "$cfg_with_list" "" --history
git -C "$g_mt" update-ref refs/heads/mt "$(mt_commit "release notes" "$email")"
run_case "history: author, committer and mergetag tagger lines are not scanned" 0 "$g_mt" "$cfg_with_list" "" --history

# G10. The asset exemption looks at the file's own extension only.
dir="$(tree_with "clean")"
mkdir -p "$dir/build.zip:0:"
printf 'x\xe2\x80\xaey\n' > "$dir/build.zip:0:/x.txt"
run_case "a folder named like an asset does not exempt its files" 1 "$dir" "$cfg_with_list" ""

# G11. Staged asset files get every rule but the invisible-character one.
dir="$(tree_with "clean")"
printf 'about %s\n' "$deny_word" > "$dir/font.woff2"
git -C "$dir" add font.woff2
printf 'clean\n' > "$dir/font.woff2"
run_case "staged deny word in an asset file" 1 "$dir" "$cfg_with_list" ""
dir="$(tree_with "clean")"
printf 'x\xe2\x80\xaey\n' > "$dir/font.woff2"
git -C "$dir" add font.woff2
run_case "staged invisible character in an asset file is fine" 0 "$dir" "$cfg_with_list" ""

# G12. Deny-list lines that can never match are refused.
for bad in bom comment latin1 tab; do
  cfg_bad="$work/cfg-$bad"
  mkdir -p "$cfg_bad/sitboard-dev"
  case "$bad" in
    bom) printf '\xef\xbb\xbf%s\n' "$deny_word" > "$cfg_bad/sitboard-dev/denylist" ;;
    comment) printf '%s # a note\n' "$deny_word" > "$cfg_bad/sitboard-dev/denylist" ;;
    latin1) printf 'caf\xc3\xa9%s\n' "$deny_word" > "$cfg_bad/sitboard-dev/denylist" ;;
    tab) printf 'ab\tcd%s\n' "$deny_word" > "$cfg_bad/sitboard-dev/denylist" ;;
  esac
  run_case "deny-list line with a $bad is refused" 2 "$clean" "$cfg_bad" ""
  hidden "deny-list $bad message does not echo the word" "orblaxian"
done

# H8. History needs Perl: a missing or failing perl fails closed.
fakebin="$work/fake-perl"
mkdir -p "$fakebin"
printf '#!/bin/sh\nexit 3\n' > "$fakebin/perl"
chmod +x "$fakebin/perl"
RUN_ENV=("PATH=$fakebin:$PATH")
run_case "history with a failing perl fails closed" 2 "$hist2" "$cfg_with_list" "" --history
nobin="$work/no-perl"
mkdir -p "$nobin"
for tool in bash git grep sed awk sort cut wc tr mktemp rm cat cp readlink xargs env dirname; do
  ln -s "$(command -v "$tool")" "$nobin/$tool"
done
RUN_ENV=("PATH=$nobin")
run_case "history without perl fails closed" 2 "$hist2" "$cfg_with_list" "" --history
want_out "missing perl message" "perl"
RUN_ENV=()

# G13. make-denylist.sh is executable, in the work tree and in git.
if [[ -x "$here/make-denylist.sh" && "$(git -C "$here" ls-files -s make-denylist.sh)" == 100755* ]]; then
  echo "ok   make-denylist.sh is executable"
else
  echo "FAIL make-denylist.sh is not executable (work tree or git mode)"
  failures=$((failures + 1))
fi

# G14. CI passes --no-denylist explicitly.
if grep -q -- '--no-denylist' "$here/../.github/workflows/ci.yml"; then
  echo "ok   CI passes --no-denylist"
else
  echo "FAIL CI does not pass --no-denylist"
  failures=$((failures + 1))
fi

# N1. The one third-party address in a licence notice that must travel
# unchanged (the Cephes notice in Go's math package) is allowed; any other
# address at that host is not.
cephes="moshier""@na-net.ornl.gov"
run_case "the Cephes notice address is allowed" 0 "$(tree_with "  $cephes")" "$cfg_with_list" ""
run_case "another address at that host is flagged" 1 "$(tree_with "someone""@na-net.ornl.gov")" "$cfg_with_list" ""

# K1. Secrets are generic rules: they run with or without the deny-list.
# Every sample is assembled from pieces so this file stays clean.
k_pem="-----BEGIN ""RSA PRIVATE KEY-----"
k_pem2="-----BEGIN ""OPENSSH PRIVATE KEY-----"
k_npm="//registry.npmjs.org/:_auth""Token=abc123def456"
k_npm2="npm""_$(printf 'Z%.0s' $(seq 1 36))"
k_slack="xox""b-1234567890-abcdefghij"
k_aws="AKIA""$(printf 'Q%.0s' $(seq 1 16))"
k_pw="db_pass""word = hunter2secret"
k_pw2="export PASS""WORD='s3cr3t-value'"
for sample in "$k_pem" "$k_pem2" "$k_npm" "$k_npm2" "$k_slack" "$k_aws" "$k_pw" "$k_pw2"; do
  run_case "secret: ${sample:0:12}..." 1 "$(tree_with "$sample")" "$cfg_empty" "" --no-denylist
  hidden "secret finding hides the value" "${sample: -6}"
done
# K2. More forms: quoted passwords in YAML, JSON, TS and Go, _PWD names,
# more token kinds, key headers in any case.
pw="Pass""word"
for sample in "db_$pw: \"hunter2secret\"" "{\"pass""word\": \"hunter2secret\"}" "const pass""wd = 'hunter2secret';" \
              "pass""word := \"hunter2secret\"" "DB_PASS""=hunter2secret" "APP_P""WD=hunter2secret" \
              "xapp""-1-A0123456789-abcdef" "https://hooks.slack"".com/services/T000/B000/XXXX" \
              "AS""IA$(printf 'R%.0s' $(seq 1 16))" "aws_secret""_access_key = abcdEFGHijklMNOP" \
              "_au""th=dXNlcjpwYXNz" "-----BEGIN PGP PRIV""ATE KEY BLOCK-----" "-----begin rsa priv""ate key-----" \
              "sk""_live_abcdefghij1234" "rk""_live_abcdefghij1234" "AI""za$(printf 'S%.0s' $(seq 1 35))"; do
  run_case "secret: ${sample:0:14}..." 1 "$(tree_with "$sample")" "$cfg_empty" "" --no-denylist
  hidden "secret finding hides the value" "${sample: -6}"
done
for fine in "pass""word: \"\${{ secrets.DB }}\"" "const pass""wordLabel = t('form.label');" "pass""word: \"<set me>\"" \
            "{\"pass""word\": \"\"}" "the pass""word: short" "type: pass""word"; do
  run_case "not a secret: ${fine:0:24}" 0 "$(tree_with "$fine")" "$cfg_empty" "" --no-denylist
done
for fine in "_auth""Token=\${NPM_TOKEN}" "pass""word = \"\"" "pass""word = \$DB_PASSWORD" "pass""word: \${{ secrets.X }}" \
            "pass""word = <your password>" "// check the pass""word field" "-----BEGIN ""PUBLIC KEY-----"; do
  run_case "not a secret: ${fine:0:20}" 0 "$(tree_with "$fine")" "$cfg_empty" "" --no-denylist
done

# S8. --help prints the usage comment only.
help_out="$(bash "$check" --help)"
if [[ "$help_out" == *"set -uo"* || "$help_out" == *"export LC_ALL"* || "$help_out" != *"--history"* ]] \
  || grep -q '^#' <<<"$help_out"; then
  echo "FAIL --help prints code or comment marks: $help_out"
  failures=$((failures + 1))
else
  echo "ok   --help prints the usage only"
fi

# S9. Finder metadata is never committed.
if grep -qxF '.DS_Store' "$here/../.gitignore"; then
  echo "ok   .gitignore has .DS_Store"
else
  echo "FAIL .gitignore lacks .DS_Store"
  failures=$((failures + 1))
fi

if ((failures > 0)); then
  echo "$failures case(s) failed"
  exit 1
fi
echo "all cases passed"
