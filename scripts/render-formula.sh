#!/usr/bin/env bash
# Print the Homebrew formula for one release, from scripts/formula/sitboard.rb.tmpl:
#
#   scripts/render-formula.sh <version> <sha macos-arm64> <sha macos-x86_64> <sha linux-arm64> <sha linux-x86_64>
#
# scripts/tap.sh calls it with the checksums of the PUBLISHED archives.
set -euo pipefail

usage="usage: render-formula.sh <version> <sha macos-arm64> <sha macos-x86_64> <sha linux-arm64> <sha linux-x86_64>"
[ $# -eq 5 ] || { echo "$usage" >&2; exit 2; }
fail() { echo "render-formula: $*" >&2; exit 1; }
version_re='^[0-9]+\.[0-9]+\.[0-9]+$'
sha_re='^[0-9a-f]{64}$'
[[ "$1" =~ $version_re ]] || fail "not a release version: $1"
for s in "$2" "$3" "$4" "$5"; do
  [[ "$s" =~ $sha_re ]] || fail "not a sha256: $s"
done

tmpl="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)/formula/sitboard.rb.tmpl"
[ -f "$tmpl" ] || fail "no template at $tmpl"
out="$(sed -e "s/{{VERSION}}/$1/g" -e "s/{{SHA_MACOS_ARM64}}/$2/g" -e "s/{{SHA_MACOS_X86_64}}/$3/g" \
           -e "s/{{SHA_LINUX_ARM64}}/$4/g" -e "s/{{SHA_LINUX_X86_64}}/$5/g" "$tmpl")"
[[ "$out" != *"{{"* ]] || fail "a placeholder in the template was not filled"
printf '%s\n' "$out"
