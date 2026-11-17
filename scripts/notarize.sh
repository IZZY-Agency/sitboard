#!/usr/bin/env bash
# Notarize a Developer ID-signed Mach-O binary with Apple's notary service and
# check that Gatekeeper accepts it. A bare executable cannot be stapled, so
# Gatekeeper fetches the ticket online the first time someone runs it.
#
#   scripts/notarize.sh <binary> <notarytool keychain profile> <submission name>
#
# Set up the profile once, beforehand:
#   xcrun notarytool store-credentials <profile> --apple-id <id> --team-id <team>
#
# The last step launches the quarantined binary once (`--version`), when this
# host can run it: its own architecture, or x86_64 on Apple Silicon with
# Rosetta. Otherwise the launch is skipped and says so; Apple's acceptance,
# codesign --check-notarization and spctl run in every case.
set -euo pipefail
BIN="${1:?usage: notarize.sh <binary> <profile> <name>}"
PROFILE="${2:?usage: notarize.sh <binary> <profile> <name>}"
NAME="${3:?usage: notarize.sh <binary> <profile> <name>}"
fail() { echo "notarize: $*" >&2; exit 1; }

codesign --verify --strict "$BIN"
# Captured first: with pipefail, `codesign | grep -q` fails when grep exits early.
INFO="$(codesign -dvv "$BIN" 2>&1)"
grep -q "Authority=Developer ID Application" <<<"$INFO" || fail "$BIN is not signed with a Developer ID Application identity"
grep -q "flags=.*runtime" <<<"$INFO" || fail "$BIN lacks the hardened runtime (codesign --options runtime)"

WORK="$(mktemp -d)" || fail "could not create a scratch folder"
WORK="$(cd "$WORK" && pwd -P)"
case "$WORK" in /?*) ;; *) fail "bad scratch folder: $WORK" ;; esac
trap 'rm -rf "$WORK"' EXIT
ZIP="$WORK/$NAME.zip"
ditto -c -k "$BIN" "$ZIP"

RESULT="$(xcrun notarytool submit "$ZIP" --keychain-profile "$PROFILE" --wait --output-format json)"
STATUS="$(/usr/bin/plutil -extract status raw -o - - <<<"$RESULT")"
ID="$(/usr/bin/plutil -extract id raw -o - - <<<"$RESULT")"
if [ "$STATUS" != "Accepted" ]; then
  echo "notarize: Apple answered $STATUS for submission $ID. Log:" >&2
  xcrun notarytool log "$ID" --keychain-profile "$PROFILE" >&2 || true
  exit 1
fi

codesign -vvvv -R="notarized" --check-notarization "$BIN" >/dev/null 2>&1 \
  || fail "accepted by Apple ($ID) but codesign --check-notarization fails for $BIN"
# `--type install` assesses a bare file; `--type execute` only judges apps.
if ! VERDICT="$(spctl --assess --type install -v "$BIN" 2>&1)" || ! grep -q "source=Notarized Developer ID" <<<"$VERDICT"; then
  fail "accepted by Apple ($ID) but Gatekeeper says: $VERDICT"
fi

host_arch="$(uname -m)"
bin_archs="$(lipo -archs "$BIN")"
rosetta() { /usr/bin/pgrep -q oahd || /usr/bin/arch -x86_64 /usr/bin/true 2>/dev/null; }
can_run=0
for arch in $bin_archs; do
  if [ "$arch" = "$host_arch" ]; then can_run=1; fi
  if [ "$arch" = x86_64 ] && [ "$host_arch" = arm64 ] && rosetta; then can_run=1; fi
done
if [ "$can_run" = 1 ]; then
  xattr -w com.apple.quarantine "0083;$(printf '%x' "$(date +%s)");notarize.sh;" "$BIN"
  if ! OUT="$(perl -e 'alarm 60; exec @ARGV' "$BIN" --version 2>&1)"; then
    xattr -d com.apple.quarantine "$BIN" || true
    fail "the quarantined binary did not start: $OUT"
  fi
  xattr -d com.apple.quarantine "$BIN"
  LAUNCH="quarantined launch printed: $OUT"
else
  LAUNCH="launch check skipped: $bin_archs binary on $host_arch"
  echo "notarize: $LAUNCH" >&2
fi
echo "notarized: submission $ID, Gatekeeper: $(grep -o 'source=.*' <<<"$VERDICT"), $LAUNCH"
