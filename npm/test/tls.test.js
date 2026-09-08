'use strict';

// Certificate verification, and the flag that must never appear.
//
// THE PROHIBITION IS ONLY HALF OF IT. An install that fails behind a
// TLS-inspecting proxy with no route forward is exactly how somebody
// ends up reaching for the flag in the first place, so the row that
// forbids it sits beside the one that proves the honest way through is
// named in the failure.

const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');

const h = require('./helpers/harness');

const V = h.VERSION;
const ASSET = `curious_${V}_linux_amd64.gz`;
const TARGET = { platform: 'linux', arch: 'x64' };

// Every file this package publishes. Read out of the manifest rather
// than listed here, so a file added to the tarball later is scanned
// without anybody remembering to add it.
function shippedSources() {
  const manifest = JSON.parse(
    fs.readFileSync(path.join(h.PACKAGE_ROOT, 'package.json'), 'utf8'));
  const files = [];
  for (const entry of manifest.files) {
    const full = path.join(h.PACKAGE_ROOT, entry);
    if (fs.existsSync(full) && fs.statSync(full).isDirectory()) {
      for (const name of fs.readdirSync(full)) {
        files.push(path.join(full, name));
      }
    } else if (entry.endsWith('.js')) {
      files.push(full);
    }
  }
  return files.filter((f) => f.endsWith('.js'));
}

test('nothing this package ships can turn certificate checking off', () => {
  const scanned = shippedSources();
  assert.ok(scanned.length >= 3, `only ${scanned.length} shipped sources were scanned`);
  for (const file of scanned) {
    const source = fs.readFileSync(file, 'utf8');
    for (const forbidden of ['rejectUnauthorized', 'NODE_TLS_REJECT_UNAUTHORIZED']) {
      assert.ok(!source.includes(forbidden),
        `${path.relative(h.PACKAGE_ROOT, file)} names ${forbidden}`);
    }
  }
  // THE PRESENCE HALF. An absence assertion is satisfied by a file
  // that says nothing at all; this is the sentence that has to be
  // there instead, and the row below proves a person actually sees it.
  const install = fs.readFileSync(path.join(h.PACKAGE_ROOT, 'install.js'), 'utf8');
  assert.match(install, /NODE_EXTRA_CA_CERTS/);
});

test('an untrusted certificate stops the install and names the way through', async (t) => {
  const ca = h.authority('trusted');
  const stranger = h.authority('stranger');
  const caFile = h.writeCA(t, ca.caPem);

  // A server presenting a chain nobody told this machine about — which
  // is what an inspecting corporate proxy looks like from here.
  const server = await h.serveAssets(t, { tlsCert: stranger });
  const dir = h.makePackage(t, { checksums: h.checksumsFor([ASSET]) });
  const run = await h.runInstall(t, dir, { base: server.origin, ca: caFile, ...TARGET });

  assert.notStrictEqual(run.code, 0, run.output);
  assert.match(run.output, /NODE_EXTRA_CA_CERTS/);
  assert.match(run.output, /certificate/i);
  assert.strictEqual(h.installedBinary(dir, 'curious'), null);
  assert.deepStrictEqual(h.leftovers(dir), []);

  // THE POSITIVE CONTROL, through the same mechanism a person behind
  // such a proxy would use: the trusted server, trusted by pointing
  // that variable at its authority, installs.
  const trusted = await h.serveAssets(t, { tlsCert: ca });
  const ok = h.makePackage(t, { checksums: h.checksumsFor([ASSET]) });
  const installed = await h.runInstall(t, ok, { base: trusted.origin, ca: caFile, ...TARGET });
  assert.strictEqual(installed.code, 0, installed.output);
  assert.deepStrictEqual(h.installedBinary(ok, 'curious'), h.BINARY_BODY);
});

test('a rejected certificate is not tried again three times', async (t) => {
  const stranger = h.authority('stranger-two');
  const server = await h.serveAssets(t, { tlsCert: stranger });
  const dir = h.makePackage(t, { checksums: h.checksumsFor([ASSET]) });
  const run = await h.runInstall(t, dir, { base: server.origin, ...TARGET });
  assert.notStrictEqual(run.code, 0, run.output);
  // A certificate that does not verify will not verify in a second's
  // time, and three seconds of waiting before saying so is three
  // seconds of a person wondering what is happening.
  assert.strictEqual(server.connections.length, 1);
});
