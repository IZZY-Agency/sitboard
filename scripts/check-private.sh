#!/usr/bin/env bash
# check-private.sh — fail when private material is about to enter the repo.
#
#   scripts/check-private.sh              scan every tracked or untracked, not ignored, file:
#                                         its content, its path (for a symlink, the target
#                                         path it stores, not the target's content), and the
#                                         lines added in the index (what is staged)
#   scripts/check-private.sh --history    scan every object reachable from any ref or HEAD:
#                                         commit messages, tag names and messages, every file
#                                         name in every tree, every file version (binary
#                                         content too, merge results too), and every ref name
#                                         (not authors, committers or taggers)
#   scripts/check-private.sh --history --scope=branch
#                                         the same, for this branch only: every object
#                                         reachable from HEAD, the local main,
#                                         origin/main and every tag (each back to its
#                                         root), and those ref names; other branches, the
#                                         stash, notes and other refs are left out, and the
#                                         report says how many. make check uses this, so
#                                         one worktree's unfinished branch cannot fail
#                                         another's. The full scan of this repository is
#                                         run by hand: make private-all, and --pull-refs
#                                         before going public. publish.sh's gate scans the
#                                         public repository's whole history after its commit
#   scripts/check-private.sh --history --pull-refs
#                                         also fetch origin's refs/pull/*/head (pull request
#                                         heads that no local ref reaches) into
#                                         refs/check-private/pull/, say how many, scan them,
#                                         and remove those refs after (exit 2 if that fails;
#                                         leftover refs there stop any later --history run)
#   scripts/check-private.sh --text FILE  scan one file, for example a pull request body
#   --no-denylist                         run the generic rules only, with or without a
#                                         deny-list (CI passes it; never implied by CI=true)
#
# Generic rules: home paths (also as Claude Code project folder names, JSON-escaped,
# URL-encoded, and Windows \Users\ or <drive>:/users/ in any case), GitHub, Anthropic, npm, Slack and AWS keys and tokens, private
# key headers, passwords set to a literal in a config-style line,
# email addresses, and invisible or bidirectional control characters: U+061C,
# U+200B to U+200F, U+202A to U+202E, U+2060, U+2066 to U+2069, U+FEFF and the tag
# characters U+E0000 to U+E007F, written below as UTF-8 byte escapes. Known binary
# assets (fonts, images, archives, PDF), by their own file extension, are not
# checked for those characters; every other rule reads them.
# Deny-list: real names, one lowercase word per line, kept OUTSIDE the repo at
#   ${XDG_CONFIG_HOME:-$HOME/.config}/sitboard-dev/denylist
# Deny-list words match anywhere, ignoring case, also inside longer words such
# as camelCase or snake_case names. A line may hold only printable ASCII and
# no ":" or "#" (a line that could never match, such as one with a byte-order
# mark, a comment or a non-ASCII letter, is refused). Without --no-denylist the
# list is required: a missing or empty list fails.
#
# The report names the place and the rule, never the matched text, so the report
# itself can be pasted anywhere. A file whose path matches a rule, or holds a
# newline or another control character, is shown as "[private path #N]", and a
# symlink's stored target as "[link target #N]", N being the file's position in
# `git ls-files --cached --others --exclude-standard`; staged lines show as
# "staged". History findings name the object ("history blob <id>:<line>") or
# "history ref names". Tree and history modes run from the repo's top level,
# wherever they are started. Every file is read as bytes (LC_ALL=C, grep -a), so
# a file that looks binary is scanned too. Replace refs and grafts are ignored
# (GIT_NO_REPLACE_OBJECTS=1, and GIT_GRAFT_FILE names a path that never
# exists), and git colour settings do not apply. --history needs perl.
# Exit 0: clean; 1: findings or no deny-list; 2: bad usage, a bad deny-list, a
# shallow clone in --history, or git or grep failed (the scan fails closed). It
# reads and writes no git setting; --pull-refs adds refs only for the run.
set -uo pipefail
export LC_ALL=C
# Read objects as they are, never as a replace ref says.
export GIT_NO_REPLACE_OBJECTS=1

denylist="${XDG_CONFIG_HOME:-$HOME/.config}/sitboard-dev/denylist"
mode="tree"
text_file=""
pull_refs=0
scope="all"
scope_note=""  # history: which refs were read, for the summary line
no_deny=0
pull_ns="refs/check-private/pull"
remove_pull_refs="git for-each-ref --format='delete %(refname)' refs/check-private/ | git update-ref --stdin"

while (($# > 0)); do
  case "$1" in
    --history) mode="history" ;;
    --pull-refs) pull_refs=1 ;;
    --scope=all|--scope=branch) scope="${1#--scope=}" ;;
    --no-denylist) no_deny=1 ;;
    --text)
      mode="text"
      text_file="${2:-}"
      [[ -n "$text_file" ]] || { echo "usage: $0 --text FILE" >&2; exit 2; }
      [[ -f "$text_file" ]] || { echo "check-private: no such file: $text_file" >&2; exit 2; }
      shift
      ;;
    -h|--help) awk 'NR == 1 { next } /^#/ { sub(/^# ?/, ""); print; next } { exit }' "$0"; exit 0 ;;
    *) echo "check-private: unknown argument: $1" >&2; exit 2 ;;
  esac
  shift
done
if ((pull_refs)) && [[ "$mode" != "history" ]]; then
  echo "check-private: --pull-refs works with --history only" >&2
  exit 2
fi
if [[ "$scope" != "all" && "$mode" != "history" ]]; then
  echo "check-private: --scope works with --history only" >&2
  exit 2
fi
if [[ "$scope" == "branch" ]] && ((pull_refs)); then
  echo "check-private: --pull-refs is for the full scan; it cannot be used with --scope=branch" >&2
  exit 2
fi

# An explicit template: macOS mktemp -d prefers its own temp folder to TMPDIR,
# and a caller (publish.sh) gives TMPDIR so its own cleanup covers this one.
tmp_raw="$(mktemp -d "${TMPDIR:-/tmp}/check-private.XXXXXX")" || { echo "check-private: cannot create a temp folder" >&2; exit 2; }
# Absolute before any cd (a relative TMPDIR gives a relative path): the scan
# and its cleanup must find the folder from the repository's top level too.
tmp="$(cd "$tmp_raw" && pwd -P)" || { rm -rf "$tmp_raw"; echo "check-private: cannot enter the temp folder" >&2; exit 2; }
# Read parents as commits record them, never as .git/info/grafts says. A path
# that never exists, not /dev/null: git prints a deprecation hint for any
# grafts file it can open.
export GIT_GRAFT_FILE="$tmp/no-grafts"
fetched_pull=0
# cleanup removes the pull request refs this run fetched. When that fails the
# run exits 2: the refs would otherwise ride along with a later push --mirror.
cleanup() {
  rm -rf "$tmp"
  if ((fetched_pull)); then
    if ! git for-each-ref --format='delete %(refname)' "$pull_ns/" | git update-ref --stdin; then
      echo "check-private: could not remove the refs under $pull_ns/; remove them with:" >&2
      echo "  $remove_pull_refs" >&2
      exit 2
    fi
  fi
}
trap cleanup EXIT

die() {
  echo "check-private: $*; nothing was checked" >&2
  exit 2
}

# grep_ok: true when the last grep exit status means "matched" or "no match".
# Anything above 1 is an error, and the scan must not report clean.
grep_ok() { (($1 <= 1)); }

# --- Deny-list -------------------------------------------------------------

use_deny=1
deny_note="deny-list used"
deny_state="present"
# deny_has <grep pattern>: true when some deny-list line matches. Fails closed.
deny_has() {
  local rc
  grep -q -e "$1" "$tmp/deny"
  rc=$?
  grep_ok "$rc" || die "cannot read the deny-list"
  return "$rc"
}
if ((no_deny)); then
  use_deny=0
  deny_note="deny-list skipped (--no-denylist)"
elif [[ -f "$denylist" ]]; then
  sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' "$denylist" > "$tmp/deny.raw" \
    || die "cannot read the deny-list"
  grep -v '^$' "$tmp/deny.raw" > "$tmp/deny"
  grep_ok $? || die "cannot read the deny-list"
  [[ -s "$tmp/deny" ]] || deny_state="empty"
  # The messages below never quote the line: it is a private word.
  # The report cuts each finding at its last ":", so a word with one would leak.
  if deny_has ':'; then
    die "a deny-list word contains ':', which the report cannot hide; remove that word from $denylist"
  fi
  if deny_has '#'; then
    die "a deny-list line contains '#'; comments are not supported and the line can never match; remove the comment from $denylist"
  fi
  # Under LC_ALL=C a non-ASCII letter only matches itself, never its other case,
  # and a byte-order mark or a control character never matches anything.
  if deny_has '[^ -~]'; then
    die "a deny-list line holds a byte outside printable ASCII (a byte-order mark, a non-ASCII letter or a control character), so it can never match; fix that line in $denylist"
  fi
else
  deny_state="missing"
fi
if ((use_deny)) && [[ "$deny_state" != "present" ]]; then
  if [[ "$deny_state" == "missing" ]]; then
    echo "check-private: deny-list missing, run scripts/make-denylist.sh" >&2
  else
    echo "check-private: deny-list is empty, run scripts/make-denylist.sh" >&2
  fi
  echo "check-private: expected at $denylist (or pass --no-denylist to run the generic rules only)" >&2
  exit 1
fi

# --- Corpus ----------------------------------------------------------------
# tree:    $tmp/names (one path per line, in ls-files order), $tmp/files
#          (NUL-separated regular files with plain names), ctrl_paths[N] (files
#          whose name holds a control character) and $tmp/link.N (the target
#          path stored by symlink N, listed in link_ids).
# history: $tmp/history (the ref names, then the scannable text of every
#          reachable object) and $tmp/history.idx ("<first line>\t<label>" for
#          each object, in order; see history_prog).

if [[ "$mode" != "text" ]]; then
  top="$(git rev-parse --show-toplevel)" || die "git rev-parse failed (is this a git repo?)"
  cd "$top" || die "cannot enter the repo's top level"
fi

ctrl_paths=()
link_ids=()
asset_exts="woff2 woff ttf otf eot png jpg jpeg gif ico webp avif pdf zip gz"
asset_excludes=()
for ext in $asset_exts; do asset_excludes+=(":(exclude,icase)*.$ext"); done
# An asset finding is "<path>.<ext>:<line>:<match>"; the match holds no ":", so
# anchoring at the end keeps a folder named like "x.zip:0:" from exempting its files.
assets_re="\.(${asset_exts// /|}):[0-9]+:[^:]*$"
history_assets_re="^history asset blob [0-9a-f]+:[0-9]+:[^:]*$"

# history_prog reads `git cat-file --batch` output and writes, for each object,
# the text the rules read: a commit's headers and message without its tree,
# parent, author and committer lines (a mergetag's tag message is read, its
# tagger line is not), a tag's headers and message without its tagger line, a
# tree's entry names one per line, a blob's bytes. Arguments: the ref-name file,
# the index file to write, the asset extensions as "a|b|c". A blob is labelled
# "asset blob" only when every file name pointing at it has an asset extension.
# shellcheck disable=SC2016 # Perl code: its $ signs are Perl's, not the shell's
history_prog='
use strict; use warnings;
my ($refs, $idxf, $exts) = @ARGV;
my $asset_re = qr/\.(?:$exts)\z/i;
binmode STDIN; binmode STDOUT;
my $line = 1; my @idx; my %asset; my %text;
sub out { my ($label, $text) = @_;
  return if $text eq "";
  $text .= "\n" if substr($text, -1) ne "\n";
  print $text or die "write failed\n";
  push @idx, [$line, $label];
  $line += ($text =~ tr/\n//);
}
open(my $rf, "<", $refs) or die "cannot read the ref names\n";
binmode $rf;
{ local $/; out("ref names", scalar(<$rf> // "")); }
close $rf;
while (defined(my $hdr = <STDIN>)) {
  chomp $hdr;
  my ($id, $type, $size) = split / /, $hdr;
  die "object $id is missing\n" unless defined $size && $size =~ /^[0-9]+$/;
  if ($type eq "blob") {
    my ($start, $left, $last) = ($line, $size, "");
    while ($left > 0) {
      my $n = read(STDIN, my $buf, $left < 1048576 ? $left : 1048576);
      die "short read\n" unless $n;
      print $buf or die "write failed\n";
      $line += ($buf =~ tr/\n//); $last = substr($buf, -1); $left -= $n;
    }
    if ($size > 0) {
      if ($last ne "\n") { print "\n" or die "write failed\n"; $line++; }
      push @idx, [$start, "blob $id"];
    }
  } else {
    my $c = "";
    if ($size > 0) { my $n = read(STDIN, $c, $size); die "short read\n" unless $n == $size; }
    if ($type eq "commit" || $type eq "tag") {
      my $i = index($c, "\n\n");
      my ($head, $msg) = $i < 0 ? ($c, "") : (substr($c, 0, $i), substr($c, $i + 2));
      my $skip = $type eq "commit" ? qr/^(?:tree|parent|author|committer) |^ tagger / : qr/^tagger /;
      my $kept = join("", map { "$_\n" } grep { $_ !~ $skip } split(/\n/, $head, -1));
      out("$type $id", "$kept$msg");
    } elsif ($type eq "tree") {
      my ($p, $hl, $names) = (0, length($id) / 2, "");
      while ($p < length $c) {
        my $sp = index($c, " ", $p); my $nul = index($c, "\0", $sp);
        die "bad tree $id\n" if $sp < 0 || $nul < 0;
        my $mode = substr($c, $p, $sp - $p);
        my $name = substr($c, $sp + 1, $nul - $sp - 1);
        my $child = unpack("H*", substr($c, $nul + 1, $hl));
        if ($mode =~ /^10/) { if ($name =~ $asset_re) { $asset{$child} = 1 } else { $text{$child} = 1 } }
        $names .= "$name\n"; $p = $nul + 1 + $hl;
      }
      out("tree $id", $names);
    } else { die "unexpected object type $type\n"; }
  }
  my $nl; read(STDIN, $nl, 1) == 1 && $nl eq "\n" or die "bad cat-file output\n";
}
open(my $xf, ">", $idxf) or die "cannot write the index\n";
for my $e (@idx) {
  my $label = $e->[1];
  $label = "asset $label" if $label =~ /^blob (\S+)$/ && $asset{$1} && !$text{$1};
  print $xf "$e->[0]\t$label\n" or die "write failed\n";
}
close $xf or die "write failed\n";
'
case "$mode" in
  tree)
    git ls-files -z --cached --others --exclude-standard > "$tmp/all" \
      || die "git ls-files failed (is this a git repo?)"
    n=0
    while IFS= read -r -d '' p; do
      n=$((n + 1))
      # A newline inside a name would shift the line numbers used as indexes.
      printf '%s\n' "${p//$'\n'/?}" >&3
      if [[ -L "$p" ]]; then
        # git stores the target path, not the target's content.
        readlink "./$p" > "$tmp/link.$n" || die "readlink failed"
        link_ids+=("$n")
      elif [[ -f "$p" ]]; then
        # grep would print such a name raw, so it is read on its own under a placeholder.
        if [[ "$p" == *[[:cntrl:]]* ]]; then
          [[ -r "$p" ]] || die "cannot read a file"
          ctrl_paths[n]="$p"
        else
          printf '%s\0' "$p" >&4
        fi
      fi
      # Anything else (a tracked file deleted from the work tree, a submodule) has no content.
    done < "$tmp/all" 3> "$tmp/names" 4> "$tmp/files"
    # What is staged can differ from the working copy; scan its added lines.
    # $tmp/staged holds every file, for every rule; $tmp/staged.text leaves
    # binary assets out, for the invisible-character rule only.
    git diff --cached --no-color --text --no-textconv --no-ext-diff > "$tmp/staged.diff" \
      || die "git diff --cached failed"
    grep -a '^+' "$tmp/staged.diff" > "$tmp/staged"
    grep_ok $? || die "grep failed on the staged diff"
    git diff --cached --no-color --text --no-textconv --no-ext-diff -- . "${asset_excludes[@]}" \
      > "$tmp/staged.text.diff" || die "git diff --cached failed"
    grep -a '^+' "$tmp/staged.text.diff" > "$tmp/staged.text"
    grep_ok $? || die "grep failed on the staged diff"
    ;;
  history)
    shallow="$(git rev-parse --is-shallow-repository)" || die "git rev-parse failed"
    [[ "$shallow" == "false" ]] \
      || die "this is a shallow clone, so its history is incomplete; run git fetch --unshallow first"
    left="$(git for-each-ref --count=1 --format='%(refname)' refs/check-private/)" \
      || die "git for-each-ref failed"
    [[ -z "$left" ]] \
      || die "refs from an earlier --pull-refs run are still under refs/check-private/; remove them with: $remove_pull_refs"
    if ((pull_refs)); then
      fetched_pull=1
      git fetch --quiet --no-tags --no-write-fetch-head origin "+refs/pull/*/head:$pull_ns/*" \
        || die "git fetch of origin's refs/pull/*/head failed"
      pulls="$(git for-each-ref --format=x "$pull_ns/" | wc -l | tr -d ' ')" || die "git for-each-ref failed"
      echo "check-private: fetched $pulls pull request head(s) from origin" >&2
      if ((pulls == 0)); then
        echo "check-private: warning: origin has no refs/pull/*/head (is origin on GitHub?); only local refs are scanned" >&2
      fi
    fi
    git for-each-ref --format='%(refname)' > "$tmp/refs.all" || die "git for-each-ref failed"
    if [[ "$scope" == "branch" ]]; then
      # HEAD (by its branch name, or HEAD when detached), main and
      # origin/main where they exist, and every tag (few, and shared on
      # purpose: their names, annotated messages and what they point at).
      # Only these refs' names are read.
      head_ref="$(git symbolic-ref -q HEAD)" || head_ref="HEAD"
      git rev-parse -q --verify "HEAD^{commit}" > /dev/null \
        || die "HEAD names no commit (a branch with no commit yet?); commit first, or run the full scan, --history"
      git for-each-ref --format='%(refname)' refs/tags/ > "$tmp/tags" || die "git for-each-ref failed"
      : > "$tmp/refs"
      tips=()
      while IFS= read -r r; do
        grep -qxF "$r" "$tmp/refs" && continue
        # A tag may point at a tree or a blob: any object will do.
        git rev-parse -q --verify "$r^{object}" > /dev/null || continue
        printf '%s\n' "$r" >> "$tmp/refs"
        tips+=("$r")
      done < <(printf '%s\n' "$head_ref" refs/heads/main refs/remotes/origin/main; cat "$tmp/tags")
      left="$(grep -cvxF -f "$tmp/refs" "$tmp/refs.all")"
      grep_ok $? || die "grep failed on the ref names"
      # Named by fixed words only: the branch's own name can hold private text.
      shown="HEAD"
      grep -qxF refs/heads/main "$tmp/refs" && shown="$shown, main"
      grep -qxF refs/remotes/origin/main "$tmp/refs" && shown="$shown, origin/main"
      shown="$shown, $(grep -c . "$tmp/tags") tag(s)"
      scope_note="branch scope: $shown; $left other ref(s) not scanned (other branches, the stash, notes), the full scan is --history"
      git rev-list --objects --no-object-names "${tips[@]}" -- > "$tmp/objects" \
        || die "git rev-list failed (is this a git repo?)"
    else
      cp "$tmp/refs.all" "$tmp/refs" || die "cp failed"
      scope_note="all refs"
      git rev-list --objects --no-object-names --all > "$tmp/objects" \
        || die "git rev-list failed (is this a git repo?)"
    fi
    git cat-file --batch < "$tmp/objects" \
      | perl -e "$history_prog" "$tmp/refs" "$tmp/history.idx" "${asset_exts// /|}" > "$tmp/history" \
      || die "reading the history failed"
    ;;
esac

# --- Matching --------------------------------------------------------------

# target is what hits searches: "content" (file content, history or text) or
# "names" (the file paths, tree mode only). staged is the staged-lines file
# (under $tmp) that tree mode reads.
target="content"
staged="staged"

# strip_drive: the one match that can hold a ":" is a Windows home (a drive
# letter, ":" and /users/<name>); drop the drive letter and its ":", so every "<place>:<line>:<match>" line
# keeps a match without ":". Only a match ends in that shape: the field before
# a match is a line number, never a letter.
# The password rule's match can hold a ":" too (YAML, JSON, Go): it is cut at
# its first ":" (after the key), so only the key part is kept.
strip_drive() {
  sed -E -e 's/[A-Za-z]:(\/[Uu][Ss][Ee][Rr][Ss]\/[A-Za-z0-9._-]*)$/\1/' \
         -e 's/(^|:)([0-9]+):(["'"'"']?[A-Za-z0-9_.-]*([Pp][Aa][Ss][Ss]|_[Pp][Ww][Dd])[^:]*):.*$/\1\2:\3/' "$1"
}

# hits <grep args...>: write every match to $tmp/hits as "<place>:<line>:<match>".
# Fails closed when git or grep fails.
hits() {
  local rc
  if [[ "$target" == "names" ]]; then
    grep -ano "$@" -- "$tmp/names" > "$tmp/raw"
    rc=$?
    grep_ok "$rc" || die "grep failed on the file names"
    # Every hit on a name is shown by position, never by the name itself.
    strip_drive "$tmp/raw" | sed 's/^\([0-9]*\):/[private path #\1]:/' > "$tmp/hits" || die "sed failed"
    return
  fi
  case "$mode" in
    tree)
      # The wrapper turns a grep error (exit above 1) into 255, which stops
      # xargs with a non-zero status. -s keeps grep's own messages, which name
      # the file, off the output. "--" ends the options before the file names
      # xargs adds, and /dev/null forces file names in the output.
      xargs -0 bash -c 'grep -sHano "$@"; (($? <= 1)) || exit 255' bash "$@" -- /dev/null \
        < "$tmp/files" > "$tmp/raw" || die "grep failed on a file (unreadable?)"
      for i in ${ctrl_paths[@]+"${!ctrl_paths[@]}"}; do
        grep -sHano --label="[private path #$i]" "$@" < "${ctrl_paths[i]}" >> "$tmp/raw"
        rc=$?
        grep_ok "$rc" || die "grep failed on a file (unreadable?)"
      done
      for i in ${link_ids[@]+"${link_ids[@]}"}; do
        grep -Hano --label="[link target #$i]" "$@" < "$tmp/link.$i" >> "$tmp/raw"
        rc=$?
        grep_ok "$rc" || die "grep failed on a link target"
      done
      grep -ano "$@" -- "$tmp/$staged" > "$tmp/raw.staged"
      rc=$?
      grep_ok "$rc" || die "grep failed on the staged lines"
      sed 's/^/staged:/' "$tmp/raw.staged" >> "$tmp/raw" || die "sed failed"
      strip_drive "$tmp/raw" | mask_private > "$tmp/hits" || die "awk failed"
      ;;
    history)
      grep -ano "$@" -- "$tmp/history" > "$tmp/raw"
      rc=$?
      grep_ok "$rc" || die "grep failed on the history"
      strip_drive "$tmp/raw" > "$tmp/raw.s" || die "sed failed"
      # Turn "<corpus line>:<match>" into "history <object>:<line in it>:<match>".
      # grep reports lines in order, so one pass over the index is enough.
      awk -F'\t' '
        FILENAME == ARGV[1] { start[++n] = $1; label[n] = $2; next }
        {
          i = index($0, ":"); l = substr($0, 1, i - 1) + 0
          while (k < n && start[k + 1] <= l) k++
          print "history " label[k] ":" (l - start[k] + 1) ":" substr($0, i + 1)
        }
      ' "$tmp/history.idx" - < "$tmp/raw.s" > "$tmp/hits" || die "awk failed"
      ;;
    text)
      grep -sano "$@" -- "$text_file" > "$tmp/raw"
      rc=$?
      grep_ok "$rc" || die "grep failed on the text file (unreadable?)"
      strip_drive "$tmp/raw" | sed 's/^/text:/' > "$tmp/hits" || die "sed failed"
      ;;
  esac
}

# mask_private: in "<file>:<line>:<match>" lines, replace a file whose path
# matched a rule (its index is in $tmp/private) by its placeholder.
mask_private() {
  awk -F: '
    FILENAME == ARGV[1] { private[$0] = 1; next }
    FILENAME == ARGV[2] { if (FNR in private) name[$0] = FNR; next }
    {
      place = substr($0, 1, length($0) - length($NF) - length($(NF - 1)) - 2)
      if (place in name) place = "[private path #" name[place] "]"
      print place ":" $(NF - 1) ":" $NF
    }
  ' "$tmp/private" "$tmp/names" -
}

# report <rule>: turn "<place>:<line>:<match>" lines into "<place>:<line>: <rule>".
report() {
  awk -F: -v rule="$1" '{ m = $NF; loc = substr($0, 1, length($0) - length(m) - 1); print loc ": " rule }'
}

# rule <label> <allowed-regex or -> <grep args...>: run one rule over the
# target and append its findings. Matches of allowed-regex are dropped.
rule() {
  local label="$1" allowed="$2" rc
  shift 2
  hits "$@"
  if [[ "$allowed" == "-" ]]; then
    cp "$tmp/hits" "$tmp/kept" || die "cp failed"
  else
    grep -viE "$allowed" "$tmp/hits" > "$tmp/kept"
    rc=$?
    grep_ok "$rc" || die "grep failed on the allow-list"
  fi
  [[ "$target" == "names" ]] && label="$label in a file name"
  report "$label" < "$tmp/kept" >> "$tmp/findings" || die "awk failed"
}

# Invisible and bidirectional control characters, as UTF-8 bytes: U+061C,
# U+200B-U+200F, U+202A-U+202E, U+2060, U+2066-U+2069, U+FEFF, and the tag
# characters U+E0000-U+E007F (f3 a0 80 80 to f3 a0 81 bf). Under LC_ALL=C both
# BSD and GNU grep read a bracket range of bytes by byte value; the test suite
# checks these and their neighbours.
unicode_pattern="$(printf '\xd8\x9c|\xe2\x80[\x8b-\x8f\xaa-\xae]|\xe2\x81[\xa0\xa6-\xa9]|\xef\xbb\xbf|\xf3\xa0[\x80\x81][\x80-\xbf]')"

# Home paths. A user segment starts with a letter, digit or "_". Only the
# Windows drive form can match a ":" (strip_drive removes it); a character
# required before a path is never ":". macOS homes are matched with a capital
# "Users" only, so web routes such as "/users/me" stay fine.
user_seg='[A-Za-z0-9_][A-Za-z0-9._-]*'
dash_seg='[A-Za-z0-9_][A-Za-z0-9._]*'
# JSON-escaped, URL-encoded, and the Claude Code project folder name (the path
# with each "/" as "-"; not inside a word).
users_other="\\\\/Users\\\\/$user_seg|%2[Ff]Users%2[Ff]$user_seg|(^|[^A-Za-z0-9:-])-Users-$dash_seg"
# Windows, in any case: \Users\ (also JSON-escaped) and a drive letter before /users/.
users_win="\\\\{1,2}users\\\\{1,2}$user_seg|(^|[^A-Za-z0-9:])[A-Za-z]:/users/$user_seg"
home_other="\\\\/home\\\\/$user_seg|%2[Ff]home%2[Ff]$user_seg|(^|[^A-Za-z0-9:-])-home-$dash_seg"
# /home/dev is the placeholder home used by test fixtures, in every spelling.
home_dev_ok=':/home/dev$|(\\/|%2f|-)home(\\/|%2f|-)dev$'

all_rules() {
  rule "home path (/Users/...)" - -oE "/Users/$user_seg"
  rule "home path (/Users/...)" - -oE "$users_other"
  rule "home path (/Users/...)" - -oiE "$users_win"
  rule "home path (/home/...)" "$home_dev_ok" -oE "/home/$user_seg"
  rule "home path (/home/...)" "$home_dev_ok" -oE "$home_other"
  rule "GitHub token" - -oE 'gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{22,}'
  rule "Anthropic key" - -oE 'sk-ant-[A-Za-z0-9_-]{20,}'
  # Keys and tokens, with or without the deny-list. A key header is "BEGIN",
  # an optional kind and "PRIVATE KEY" (any case, PGP blocks too); a public
  # key is fine.
  rule "private key" - -oiE -e "-----BEGIN ([A-Z0-9]+ )*PRIV""ATE KEY( BLOCK)?-----"
  rule "npm token" - -oE -e "_auth(Token)?[[:space:]]*=[[:space:]]*[^[:space:]\$][^[:space:]]*|npm""_[A-Za-z0-9]{36}"
  rule "Slack token" - -oE -e "xox""[abpors]-[A-Za-z0-9-]{10,}|xa""pp-[A-Za-z0-9-]{10,}|hooks\\.slack\\.com/services/[A-Za-z0-9/_-]+"
  rule "AWS key" - -oE -e "(AK""IA|AS""IA)[0-9A-Z]{16}"
  rule "AWS key" - -oiE -e "aws_secret""_access_key[[:space:]]*=[[:space:]]*[^[:space:]\$][^[:space:]]*"
  rule "Stripe key" - -oE -e "(sk|rk)_li""ve_[0-9A-Za-z]{10,}"
  rule "Google API key" - -oE -e "AI""za[0-9A-Za-z_-]{35}"
  # A password set to a literal: not empty, not a variable ($X, ${X}), not a
  # placeholder (<...>, {{...}}). Config lines (name=value at line start,
  # also DB_PASS= and _PWD= names), and quoted literals of 6+ characters after
  # a password key with :, = or := (YAML, JSON, TS, Go).
  # A key is ...password..., ...passwd..., or ends in _pass or _pwd (DB_PASS):
  # never a word that merely starts with pass (passUntil, ASKPASS).
  pw_key="([A-Za-z0-9_.-]*pass""(word|wd)[A-Za-z0-9_.-]*|[A-Za-z0-9_.-]*_(pass|pwd))"
  rule "password" - -oiE -e "^[[:space:]]*(export[[:space:]]+)?${pw_key}[[:space:]]*=[[:space:]]*[\"']?[^[:space:]\"'\$<{=][^[:space:]\"']*"
  rule "password" - -oiE -e "[\"']?${pw_key}[\"']?[[:space:]]*(:=|:|=)[[:space:]]*[\"'][^\"'\$<{[:space:]][^\"']{5,}[\"']"
  # moshier@na-net.ornl.gov is in the Cephes notice that Go's math package
  # carries; THIRD_PARTY_NOTICES.md must reproduce it unchanged.
  rule "email address" \
    '(@example\.com|@users\.noreply\.github\.com|:git@github\.com|:noreply@anthropic\.com|:moshier@na-net\.ornl\.gov)$' \
    -oE '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}'
  local exempt="$assets_re"
  [[ "$mode" == "history" ]] && exempt="$history_assets_re"
  staged="staged.text"
  rule "invisible or bidirectional control character" "$exempt" -oE "$unicode_pattern"
  staged="staged"
  if ((use_deny)); then
    rule "deny-list word" - -oiF -f "$tmp/deny"
  fi
}

: > "$tmp/findings"
: > "$tmp/private"
if [[ "$mode" == "tree" ]]; then
  target="names"
  all_rules
  sed -n 's/^\[private path #\([0-9]*\)\].*/\1/p' "$tmp/findings" | sort -u > "$tmp/private" \
    || die "sed failed"
  target="content"
fi
all_rules

sort -u "$tmp/findings" > "$tmp/report" || die "sort failed"

if [[ -s "$tmp/report" ]]; then
  cat "$tmp/report"
  echo "check-private: $(wc -l < "$tmp/report" | tr -d ' ') finding(s) in $mode mode${scope_note:+ ($scope_note)}; $deny_note" >&2
  exit 1
fi
echo "check-private: clean ($mode mode${scope_note:+, $scope_note}; $deny_note)"
