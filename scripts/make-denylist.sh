#!/usr/bin/env bash
# make-denylist.sh — build the private-name deny-list used by check-private.sh.
#
#   scripts/make-denylist.sh            add new words; keep the words already in the list
#   scripts/make-denylist.sh --rebuild  also re-filter the words already in the list
#
# Writes ${XDG_CONFIG_HOME:-$HOME/.config}/sitboard-dev/denylist, OUTSIDE the repo,
# one lowercase word per line, mode 0600. Words already in the file are kept as
# they are, so a short name added by hand survives a rerun; the run says how many
# of them are under 6 letters. --rebuild applies the filters below to them too.
# An existing list that cannot be read stops the run, so no word is lost.
# Sources, each optional:
#   - folder names under ~/.claude/projects
#   - working folders recorded in ~/.codex/sessions
#   - workspace and session names from `agtermctl tree --json`
#   - the login name
# check-private.sh matches these words anywhere inside longer words, so new words
# shorter than 6 letters, numbers, common words and public names (tools,
# platforms, this project) are dropped. Review the result by hand: remove
# ordinary words, add short real names.
# It prints only a count, never the words. It reads and writes no git setting.
set -euo pipefail

rebuild=0
case "${1:-}" in
  "") ;;
  --rebuild) rebuild=1 ;;
  *) echo "usage: $0 [--rebuild]" >&2; exit 2 ;;
esac

out_dir="${XDG_CONFIG_HOME:-$HOME/.config}/sitboard-dev"
out="$out_dir/denylist"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

raw="$tmp/raw"
: > "$raw"

if [[ -d "$HOME/.claude/projects" ]]; then
  find "$HOME/.claude/projects" -mindepth 1 -maxdepth 1 -type d -exec basename {} \; >> "$raw"
fi

if [[ -d "$HOME/.codex/sessions" ]]; then
  # The first line of each rollout is its session_meta record, which holds "cwd".
  find "$HOME/.codex/sessions" -type f -name 'rollout-*.jsonl' -exec head -n 1 {} \; 2>/dev/null \
    | grep -oE '"cwd" *: *"[^"]*"' >> "$raw" || true
fi

if command -v agtermctl >/dev/null 2>&1; then
  agtermctl tree --json 2>/dev/null | grep -oE '"name" *: *"[^"]*"' >> "$raw" || true
fi

id -un >> "$raw" 2>/dev/null || true

# Common words that are not names. Lower case.
cat > "$tmp/stop" <<'EOF'
about
after
agent
agents
also
analytics
android
apps
archive
assets
backend
backup
before
build
cache
claude
client
code
codex
components
config
content
dashboard
data
default
deploy
design
desktop
develop
development
dist
docs
documents
downloads
experiments
feature
fix
from
frontend
github
home
homepage
into
landing
legacy
library
local
main
marketing
master
migration
migrations
misc
mobile
monitoring
notes
onboarding
personal
plan
plans
playground
plugin
plugins
private
production
project
projects
prototype
public
release
repo
repos
research
review
sandbox
scratch
scripts
server
service
services
session
sessions
settings
shared
sitboard
site
spike
src
staging
support
temp
template
templates
test
tests
that
this
tmp
tools
upgrade
user
users
website
with
work
worker
workspace
workspaces
worktree
worktrees
www
EOF

# Public names: tools, platforms and this project. Not private, and common in
# this repo's code and docs.
cat > "$tmp/public" <<'EOF'
agterm
agtermctl
anthropic
apple
bitbucket
claude
cloudflare
codex
docker
fsnotify
ghostty
github
gitlab
golang
golangci
google
homebrew
iterm
javascript
jetbrains
linkrypt
linux
macos
node
nodejs
openai
python
ration
react
rustc
sitboard
svelte
typescript
ubuntu
vercel
vite
vscode
EOF

mkdir -p "$out_dir"
chmod 700 "$out_dir"

# Words already in the list (blank lines dropped). A list that exists but cannot
# be read stops the run: writing a new one would lose the words added by hand.
: > "$tmp/existing"
if [[ -e "$out" ]]; then
  if ! cat "$out" > "$tmp/existing.raw" 2>/dev/null; then
    echo "make-denylist: cannot read the existing list at $out; fix its permissions, nothing was changed" >&2
    exit 1
  fi
  grep -v '^[[:space:]]*$' "$tmp/existing.raw" > "$tmp/existing" || true
fi

# filter: keep words of 6+ letters that are not numbers, common words or public names.
filter() {
  grep -E '^[a-z0-9]{6,}$' \
    | grep -vE '^[0-9]+$' \
    | grep -vxF -f "$tmp/stop" \
    | grep -vxF -f "$tmp/public" || true
}

short="$(grep -cvE '^.{6,}$' "$tmp/existing" || true)"
{
  if ((rebuild)); then filter < "$tmp/existing"; else cat "$tmp/existing"; fi
  # ASCII on purpose (LC_ALL=C in check-private.sh): only ASCII letters are folded.
  # shellcheck disable=SC2018,SC2019
  sed -E 's/"[a-z]+" *: *"//; s/"$//' "$raw" \
    | tr -cs 'A-Za-z0-9' '\n' \
    | tr 'A-Z' 'a-z' \
    | filter
} | sort -u > "$tmp/denylist"

umask 077
cp "$tmp/denylist" "$out.tmp"
chmod 600 "$out.tmp"
mv "$out.tmp" "$out"
echo "make-denylist: $(wc -l < "$out" | tr -d ' ') words in $out"
if ((rebuild)); then
  echo "make-denylist: existing words re-filtered (--rebuild)"
elif ((short > 0)); then
  echo "make-denylist: $short existing word(s) under 6 letters were kept; they match inside longer words. Rerun with --rebuild to drop them, then add back any real short name by hand"
fi
echo "make-denylist: review the list by hand: remove ordinary words (they match inside longer words), add short real names"
