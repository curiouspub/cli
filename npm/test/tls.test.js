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

test('a certificate whose issuer is nowhere still names the way through', async (t) => {
  // THE COMMONEST CERTIFICATE FAILURE OF ALL, and the one the classifier
  // could not see. A host that serves its own certificate and nothing
  // beside it leaves the client with a signature it cannot check against
  // anything — which is what an inspecting proxy looks like from here far
  // more often than a chain does — and the platform reports that with a
  // code carrying no CERT in its name.
  //
  // Unclassified, it read as a fault worth trying again: three attempts,
  // three waits, and then a message about the network with the one
  // variable a person can act on nowhere in it. THE ROW ASSERTS THE CODE
  // rather than the shape of the failure, because a row that asked only
  // for the word "certificate" passes against a different one.
  const trusted = h.authority('issuer-known');
  const caFile = h.writeCA(t, trusted.caPem);
  const stranger = h.authority('issuer-nowhere');

  const server = await h.serveAssets(t, {
    tlsCert: { certPem: stranger.leafPem, keyPem: stranger.keyPem },
  });
  const dir = h.makePackage(t, { checksums: h.checksumsFor([ASSET]) });
  const run = await h.runInstall(t, dir, { base: server.origin, ca: caFile, ...TARGET });

  assert.notStrictEqual(run.code, 0, run.output);
  assert.match(run.output, /UNABLE_TO_VERIFY_LEAF_SIGNATURE/);
  assert.match(run.output, /NODE_EXTRA_CA_CERTS/);
  // A certificate nobody can verify will not verify in a second's time,
  // and the count is where "this was classified" is observable.
  assert.strictEqual(server.connections.length, 1,
    `the same certificate was fetched ${server.connections.length} times`);
  assert.strictEqual(h.installedBinary(dir, 'curious'), null);
  assert.deepStrictEqual(h.leftovers(dir), []);

  // THE POSITIVE CONTROL, and it is the same server with its issuer put
  // where the client can see it: the leaf alone, trusted through the
  // authority that signed it, installs.
  const ownCA = h.writeCA(t, stranger.caPem);
  const ok = h.makePackage(t, { checksums: h.checksumsFor([ASSET]) });
  const installed = await h.runInstall(t, ok, { base: server.origin, ca: ownCA, ...TARGET });
  assert.strictEqual(installed.code, 0, installed.output);
  assert.deepStrictEqual(h.installedBinary(ok, 'curious'), h.BINARY_BODY);
});

test('an address literal on the direct route is checked against the certificate', async (t) => {
  // NO PROXY, so the identity check happens where the request is made
  // rather than on a tunnel somebody else opened. It is a second call
  // site asking the same question, and the platform defect install.js
  // repairs — an IPv6 literal never reaching the certificate's address
  // entries, between Node v22.23.2 and v24.20.0 — is in the question
  // rather than in either call site. A repair at one of them leaves
  // half this package's routes broken on its own declared floor.
  const ca = h.authority('direct-literal');
  const caFile = h.writeCA(t, ca.caPem);
  const server = await h.serveAssets(t, { tlsCert: ca, host: '::1' });
  assert.ok(server.origin.startsWith('https://[::1]:'), server.origin);

  const dir = h.makePackage(t, { checksums: h.checksumsFor([ASSET]) });
  const run = await h.runInstall(t, dir, { base: server.origin, ca: caFile, ...TARGET });

  assert.strictEqual(run.code, 0, run.output);
  // Bracketed in the URL, bare at the certificate, and never a name.
  assert.strictEqual(server.requests[0].servername, null);
  assert.deepStrictEqual(h.installedBinary(dir, 'curious'), h.BINARY_BODY);
});

test('a certificate that names somewhere else is refused at an address literal too', async (t) => {
  // THE OTHER SIDE OF THE REPAIR, and the reason it needs one.
  // install.js supplies its own identity check for an address literal,
  // because the platform between v22.23.2 and v24.20.0 cannot make that
  // comparison — and a supplied identity check is the single hook that
  // can turn verification off without ever naming rejectUnauthorized,
  // which is what the grep row above watches for. The absence row
  // cannot see this; only running it can.
  //
  // So: a certificate carrying no entry for this address, presented at
  // this address, on the route the repair sits on.
  const ca = h.authority('elsewhere', [], ['other.example']);
  const caFile = h.writeCA(t, ca.caPem);
  const server = await h.serveAssets(t, { tlsCert: ca, host: '::1' });
  const dir = h.makePackage(t, { checksums: h.checksumsFor([ASSET]) });

  const run = await h.runInstall(t, dir, { base: server.origin, ca: caFile, ...TARGET });

  assert.notStrictEqual(run.code, 0, run.output);
  assert.match(run.output, /certificate/i);
  assert.strictEqual(h.installedBinary(dir, 'curious'), null);
  assert.deepStrictEqual(h.leftovers(dir), []);
});
