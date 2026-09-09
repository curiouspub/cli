'use strict';

// Redirects, which the real release host uses to hand a download over
// to whatever is actually storing the file.
//
// THE SCHEME RULE IS ONE RULE AND IT APPLIES TO EVERY HOP. Two rules —
// one for the first address and a stricter one for redirects — is what
// makes a loopback server issuing a redirect abort on its own redirect,
// which is the ordinary way anybody would exercise this locally.

const test = require('node:test');
const assert = require('node:assert');

const h = require('./helpers/harness');

const V = h.VERSION;
const ASSET = `curious_${V}_linux_amd64.gz`;
const TARGET = { platform: 'linux', arch: 'x64' };

test('an https redirect is followed to a successful install', async (t) => {
  const ca = h.authority('redirect');
  const caFile = h.writeCA(t, ca.caPem);
  const server = await h.serveAssets(t, {
    tlsCert: ca,
    handler: (record, res) => {
      if (record.url.endsWith('.gz')) {
        res.writeHead(302, { location: '/stored/object' });
        res.end();
        return;
      }
      res.writeHead(200, { 'content-length': String(h.BINARY_GZ.length) });
      res.end(h.BINARY_GZ);
    },
  });
  const dir = h.makePackage(t, { checksums: h.checksumsFor([ASSET]) });
  const run = await h.runInstall(t, dir, { base: server.origin, ca: caFile, ...TARGET });

  // THE POSITIVE CONTROL FOR THE WHOLE FILE. Without it, a client that
  // refused every redirect would pass every negative row below.
  assert.strictEqual(run.code, 0, run.output);
  assert.deepStrictEqual(h.installedBinary(dir, 'curious'), h.BINARY_BODY);
  assert.deepStrictEqual(server.requests.map((r) => r.url),
    [`/v${V}/${ASSET}`, '/stored/object']);
});

test('a relative Location is resolved against the address that sent it', async (t) => {
  const server = await h.serveAssets(t, {
    handler: (record, res) => {
      if (record.url.endsWith('.gz')) {
        // No scheme, no host: only a client that resolves this against
        // the current URL asks for anything at all.
        res.writeHead(307, { location: '../elsewhere/object' });
        res.end();
        return;
      }
      res.writeHead(200, { 'content-length': String(h.BINARY_GZ.length) });
      res.end(h.BINARY_GZ);
    },
  });
  const dir = h.makePackage(t, { checksums: h.checksumsFor([ASSET]) });
  const run = await h.runInstall(t, dir, { base: server.origin, ...TARGET });
  assert.strictEqual(run.code, 0, run.output);
  assert.deepStrictEqual(server.requests.map((r) => r.url),
    [`/v${V}/${ASSET}`, '/elsewhere/object']);
});

test('a redirect to plaintext off this machine stops before anything is written', async (t) => {
  const server = await h.serveAssets(t, {
    handler: (record, res) => {
      res.writeHead(302, { location: 'http://storage.invalid/object' });
      res.end();
    },
  });
  const dir = h.makePackage(t, { checksums: h.checksumsFor([ASSET]) });
  const run = await h.runInstall(t, dir, { base: server.origin, ...TARGET });
  assert.notStrictEqual(run.code, 0, run.output);
  assert.match(run.output, /https/);
  assert.match(run.output, /storage\.invalid/);
  assert.strictEqual(h.installedBinary(dir, 'curious'), null);
  assert.deepStrictEqual(h.leftovers(dir), []);
  // Refused rather than repeatedly refused: a rule that will say the
  // same thing in a second is not worth waiting three seconds for.
  assert.strictEqual(server.requests.length, 1);
});

test('a redirect loop ends at the bound rather than running forever', async (t) => {
  const server = await h.serveAssets(t, {
    handler: (record, res) => {
      res.writeHead(302, { location: `/v${V}/${ASSET}` });
      res.end();
    },
  });
  const dir = h.makePackage(t, { checksums: h.checksumsFor([ASSET]) });
  const run = await h.runInstall(t, dir, { base: server.origin, ...TARGET });
  assert.notStrictEqual(run.code, 0, run.output);
  assert.match(run.output, /redirect/i);
  // The bound is what stops it: the count is finite and small, and the
  // row asserts the number rather than merely "it ended".
  assert.strictEqual(server.requests.length, 6);
  assert.deepStrictEqual(h.leftovers(dir), []);
});

test('a redirect with no Location is a failure rather than a retry', async (t) => {
  const server = await h.serveAssets(t, {
    handler: (record, res) => {
      res.writeHead(302);
      res.end();
    },
  });
  const dir = h.makePackage(t, { checksums: h.checksumsFor([ASSET]) });
  const run = await h.runInstall(t, dir, { base: server.origin, ...TARGET });
  assert.notStrictEqual(run.code, 0, run.output);
  assert.match(run.output, /redirect/i);
  assert.strictEqual(server.requests.length, 1);
});

test('a Location that is not a URL is a failure rather than a retry', async (t) => {
  const server = await h.serveAssets(t, {
    handler: (record, res) => {
      res.writeHead(302, { location: 'http://[not a host]/object' });
      res.end();
    },
  });
  const dir = h.makePackage(t, { checksums: h.checksumsFor([ASSET]) });
  const run = await h.runInstall(t, dir, { base: server.origin, ...TARGET });
  assert.notStrictEqual(run.code, 0, run.output);
  assert.strictEqual(server.requests.length, 1);
  assert.deepStrictEqual(h.leftovers(dir), []);
});

// A redirect whose body never ends, which is the shape that tells apart
// draining a response from closing it.
//
// TWO NUMBERS, BOTH CHOSEN HERE. The body keeps arriving for at most two
// seconds: that is how long this row is willing to spend proving that a
// client is STILL reading something it said it was finished with, and it
// is spent only on the way to failing. One chunk every five
// milliseconds keeps the stream alive without making the row a memory
// test. Neither is a bound on the installer, which has its own and is
// nowhere near either.
const ENDLESS_BODY_MS = 2_000;
const CHUNK_EVERY_MS = 5;

test('a redirect body that never ends is closed rather than read to nowhere', async (t) => {
  let body = null;
  const server = await h.serveAssets(t, {
    handler: (record, res) => {
      if (record.url.endsWith('.gz')) {
        res.writeHead(302, { location: '/stored/object' });
        body = h.keepSending(res, { forMs: ENDLESS_BODY_MS, everyMs: CHUNK_EVERY_MS });
        return;
      }
      res.writeHead(200, { 'content-length': String(h.BINARY_GZ.length) });
      res.end(h.BINARY_GZ);
    },
  });

  const dir = h.makePackage(t, { checksums: h.checksumsFor([ASSET]) });
  const run = await h.runInstall(t, dir, { base: server.origin, ...TARGET });

  // THE PRESENCE HALF: the redirect was followed and the binary that
  // landed is the right one. Without it, a client that refused every
  // redirect outright would also hang up.
  assert.strictEqual(run.code, 0, run.output);
  assert.deepStrictEqual(h.installedBinary(dir, 'curious'), h.BINARY_BODY);
  assert.deepStrictEqual(server.requests.map((r) => r.url),
    [`/v${V}/${ASSET}`, '/stored/object']);

  // AND THE ABSENCE: nobody was still reading it.
  assert.strictEqual(body.hungUp, true,
    "the endless redirect body was read to the end of this row's patience, " +
    `${body.chunks} chunks of it`);
});
