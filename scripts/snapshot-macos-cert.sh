#!/usr/bin/env bash
# A throwaway code-signing chain for the snapshot build, so the macOS
# signing path runs on every pull request instead of first running inside
# a release somebody has already approved.
#
# TWO CERTIFICATES, NOT ONE, and that is the signer's rule rather than a
# preference. It refuses a bundle holding a single certificate ("full
# certificate chain not present") and then looks the chain up among the
# platform vendor's own authorities, which a throwaway can never be among.
# A generated root plus a code-signing leaf verifies against itself.
#
# Nothing here is a secret. The key lives for one build, signs nothing
# anybody installs, and chains to a root no machine trusts, so the
# password is a constant for the same reason the snapshot's cosign key
# pair has one.
set -euo pipefail

out="${1:?usage: scripts/snapshot-macos-cert.sh <output directory>}"
mkdir -p "$out"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

openssl req -x509 -newkey rsa:2048 -nodes -days 2 \
  -subj "/CN=curious snapshot signing root" \
  -addext "basicConstraints=critical,CA:TRUE" \
  -addext "keyUsage=critical,keyCertSign" \
  -keyout "$work/root.key" -out "$work/root.pem" 2>/dev/null

openssl req -newkey rsa:2048 -nodes \
  -subj "/CN=curious snapshot signing" \
  -keyout "$work/leaf.key" -out "$work/leaf.csr" 2>/dev/null

printf '%s\n' \
  "basicConstraints=critical,CA:FALSE" \
  "keyUsage=critical,digitalSignature" \
  "extendedKeyUsage=critical,codeSigning" > "$work/leaf.ext"

openssl x509 -req -in "$work/leaf.csr" -days 2 \
  -CA "$work/root.pem" -CAkey "$work/root.key" -CAcreateserial \
  -extfile "$work/leaf.ext" -out "$work/leaf.pem" 2>/dev/null

openssl pkcs12 -export -passout pass:snapshot \
  -inkey "$work/leaf.key" -in "$work/leaf.pem" -certfile "$work/root.pem" \
  -out "$out/macos.p12"
