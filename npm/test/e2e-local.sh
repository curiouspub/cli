#!/usr/bin/env bash
#
# The whole path, end to end, against a server on this machine: build
# the real binary, publish it the way a release would, pack the wrapper,
# install the tarball, and run the command that comes out.
#
# WHY IT IS NOT PART OF THE GATE. It builds the real binary and stands
# up a server, which is minutes rather than seconds. Everything it
# covers that can be covered faster already is; what only this can show
# is the join — that the package manager's own install, with this
# package's own postinstall, produces a working command.
#
# NOT THROUGH A BARE RUN-WITHOUT-INSTALLING FORM, and this is the sharp
# edge. That form resolves by COMMAND name first: a temporary prefix
# exposes `curious`, not `curiouspub`, so the runner would find nothing
# locally and fetch the package from the real registry — testing the
# wrong package, from the wrong place, and failing for a reason
# unconnected to anything here. The bare form is for a registry-backed
# run. Here the tarball is named explicitly.

set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
package="$(cd "${here}/.." && pwd)"
root="$(cd "${package}/.." && pwd)"

base="${CURIOUS_RELEASE_BASE_URL:-http://127.0.0.1:8099}"
port="${base##*:}"
port="${port%%/*}"

work="$(mktemp -d)"
served="${work}/release"
staging="${work}/package"
prefix="${work}/prefix"
server_pid=""

cleanup() {
  if [ -n "${server_pid}" ]; then
    kill "${server_pid}" 2>/dev/null || true
  fi
  rm -rf "${work}"
}
trap cleanup EXIT

version="$(node -p "require('${package}/package.json').version")"
asset="$(node -e "
  const p = require('${package}/lib/platform.js');
  const name = p.assetName('${version}', process.platform, process.arch);
  if (!name) { console.error('this machine is not a platform the wrapper supports'); process.exit(1); }
  process.stdout.write(name);
")"

echo "==> building the real binary"
mkdir -p "${served}/v${version}" "${staging}" "${prefix}"
go build -trimpath -o "${work}/curious" "${root}/cmd/curious"

echo "==> publishing it the way a release would: one gzip, one digest"
node -e "
  const fs = require('node:fs');
  const zlib = require('node:zlib');
  const crypto = require('node:crypto');
  const gz = zlib.gzipSync(fs.readFileSync('${work}/curious'));
  fs.writeFileSync('${served}/v${version}/${asset}', gz);
  const digest = crypto.createHash('sha256').update(gz).digest('hex');
  fs.writeFileSync('${work}/checksums.txt', digest + '  ${asset}\n');
"

echo "==> staging the package with that digest in it"
# A copy, so a run of this script never leaves a real checksum table in
# the working tree — the one that is committed is deliberately empty and
# is written by the release.
tar -cf - -C "${package}" \
  bin lib install.js package.json loopback-hosts.json README.md LICENSE |
  tar -xf - -C "${staging}"
node -e "
  const fs = require('node:fs');
  const line = fs.readFileSync('${work}/checksums.txt', 'utf8').trim().split(/\s+/);
  fs.writeFileSync('${staging}/checksums.json',
    JSON.stringify({ [line[1]]: line[0] }, null, 2) + '\n');
"

echo "==> serving ${served} on ${base}"
node -e "
  const http = require('node:http');
  const fs = require('node:fs');
  const path = require('node:path');
  http.createServer((req, res) => {
    const file = path.join('${served}', decodeURIComponent(req.url.split('?')[0]));
    if (!file.startsWith('${served}') ||
        !fs.existsSync(file) || !fs.statSync(file).isFile()) {
      // A 404 rather than a crash: the readiness probe below asks for
      // the root, which is a directory, and a server that dies on it
      // fails the install several steps later with a message about the
      // network.
      res.writeHead(404).end();
      return;
    }
    const body = fs.readFileSync(file);
    res.writeHead(200, { 'content-length': String(body.length) });
    res.end(body);
  }).listen(${port}, '127.0.0.1', () => console.log('serving'));
" &
server_pid=$!

for _ in $(seq 1 50); do
  if node -e "
    require('node:http').get('${base}/', (r) => process.exit(0)).on('error', () => process.exit(1));
  " 2>/dev/null; then
    break
  fi
  sleep 0.1
done

echo "==> packing"
tarball="$(cd "${staging}" && npm pack --pack-destination "${work}" --silent | tail -n1)"
tarball="${work}/${tarball}"
test -f "${tarball}"

echo "==> installing ${tarball} into a temporary prefix"
CURIOUS_RELEASE_BASE_URL="${base}" \
  npm install --prefix "${prefix}" --no-audit --no-fund "${tarball}"

echo "==> running the command the package installed"
installed="${prefix}/node_modules/.bin/curious"
test -x "${installed}"
"${installed}" version

echo "==> and through the package manager's own runner, by tarball name"
# --yes, or an unattended run stops at the install prompt. --package
# names the tarball, so nothing is resolved from a registry.
CURIOUS_RELEASE_BASE_URL="${base}" \
  npm exec --yes --prefix "${prefix}" --package "${tarball}" -- curious version

echo
echo "OK: packed, installed, verified against the embedded digest, and ran."
