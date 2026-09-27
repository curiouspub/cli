#!/usr/bin/env bash
# Ask the operating system whether it would let each named macOS binary
# run once downloaded, and fail unless every answer is yes, from a
# notarised developer signature.
#
# THE ASSESSMENT TYPE IS "install", NOT THE DEFAULT. The default type
# expects an application bundle and rejects every command-line tool,
# however it is signed: a notarised developer-signed tool comes back
# "rejected (the code is valid but does not seem to be an app)". So the
# default answers a question nobody asked, and it answers no.
#
# "ACCEPTED" IS NOT ENOUGH ON ITS OWN. The source line is what separates a
# notarised binary from one that is only signed, and notarisation is the
# half the package manager's quarantine actually turns on. So both lines
# are required.
#
# macOS only. The release workflow runs it on a hosted macOS runner, and
# anyone holding a downloaded archive can run it on theirs.
set -euo pipefail

if [ "$#" -eq 0 ]; then
  echo "usage: scripts/gatekeeper-check.sh <binary>..." >&2
  exit 2
fi

failed=0
for bin in "$@"; do
  out="$(spctl -a -vv -t install "$bin" 2>&1)" || true
  printf '%s\n' "$out"
  if printf '%s\n' "$out" | grep -q ': accepted$' &&
     printf '%s\n' "$out" | grep -qx 'source=Notarized Developer ID'; then
    echo "gatekeeper-check: $bin is accepted, notarised"
  else
    echo "gatekeeper-check: $bin would be refused once downloaded: it needs to be accepted, from source=Notarized Developer ID" >&2
    failed=1
  fi
done
exit "$failed"
