#!/usr/bin/env bash
# End-to-end tests of the notices build (make notices-check runs them, after
# the web dependencies are installed):
#   - a page that imports a fake npm package's stylesheet and font: the
#     package gets an entry when the font is a file, and an inlined font
#     (a data: URI in the CSS) stops the generator
#   - -check fails on a notices file that is out of date
# shellcheck disable=SC2015 # ok() always returns 0
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repo="$(cd "$here/.." && pwd -P)"
[ -d "$repo/web/node_modules/vite" ] || { echo "notices_test: run make web-deps first" >&2; exit 1; }
work="$(mktemp -d)" || exit 2
work="$(cd "$work" && pwd -P)"
case "$work" in /?*) ;; *) echo "notices_test: bad scratch folder" >&2; exit 2 ;; esac
trap 'rm -rf "$work"' EXIT

failures=0
ok()  { echo "ok   $1"; }
bad() { echo "FAIL $1"; printf '%s\n' "$2" | sed 's/^/    /'; failures=$((failures + 1)); }

# fake_page <name> <font bytes>: a page whose script imports fakepkg's
# stylesheet, which uses fakepkg's font.
fake_page() {
  local w="$work/$1"
  mkdir -p "$w/src" "$w/node_modules/fakepkg"
  ln -s "$repo/web/node_modules/vite" "$w/node_modules/vite"
  printf '<!doctype html>\n<script type="module" src="/src/main.js"></script>\n' > "$w/index.html"
  # As in web/vite.config.ts: no modulepreload polyfill.
  printf 'export default { build: { modulePreload: { polyfill: false } } };\n' > "$w/vite.config.ts"
  printf "import 'fakepkg/style.css';\nconsole.log('page');\n" > "$w/src/main.js"
  printf '{"name":"fakepkg","version":"1.0.0","license":"MIT","repository":"github:example/fakepkg"}\n' > "$w/node_modules/fakepkg/package.json"
  cp "$repo/internal/tools/notices/testdata/mit.txt" "$w/node_modules/fakepkg/LICENSE"
  printf "@font-face { font-family: Fake; src: url('./font.woff') format('woff'); }\nbody { font-family: Fake; }\n" > "$w/node_modules/fakepkg/style.css"
  head -c "$2" /dev/zero > "$w/node_modules/fakepkg/font.woff"
  echo "$w"
}
generate() {  # <web> <out folder> [generator args...]
  local w="$1" o="$2"
  shift 2
  node "$here/notices-bundle.mjs" "$w" "$o" > "$o.log" 2>&1 || { cat "$o.log"; return 3; }
  (cd "$repo" && go run ./internal/tools/notices -bundle "$o" -web "$w" "$@") 2>&1
}

w="$(fake_page small 200)"
out="$(generate "$w" "$work/small" -out "$work/small.md")"; rc=$?
[[ "$rc" == 1 && "$out" == *"inlines a font or image"* ]] \
  && ok "a font inlined from an npm package's stylesheet stops the generator" || bad "inlined font" "exit $rc: $out"

w="$(fake_page large 9000)"
out="$(generate "$w" "$work/large" -out "$work/large.md")"; rc=$?
if [[ "$rc" == 0 ]] && grep -q '^## fakepkg 1.0.0$' "$work/large.md"; then
  ok "an npm package with only a stylesheet and a font gets an entry"
else
  bad "stylesheet and font package" "exit $rc: $out"
fi

cp "$repo/THIRD_PARTY_NOTICES.md" "$work/stale.md"
printf '\nedited by hand\n' >> "$work/stale.md"
out="$(generate "$repo/web" "$work/real" -out "$work/stale.md" -check)"; rc=$?
[[ "$rc" == 1 && "$out" == *"is out of date"* ]] && ok "-check fails on a notices file that is out of date" || bad "-check on drift" "exit $rc: $out"
out="$(generate "$repo/web" "$work/real2" -out "$repo/THIRD_PARTY_NOTICES.md" -check)"; rc=$?
[[ "$rc" == 0 ]] && ok "-check passes on the committed file" || bad "-check on the committed file" "exit $rc: $out"

if ((failures > 0)); then
  echo "$failures case(s) failed"
  exit 1
fi
echo "all cases passed"
