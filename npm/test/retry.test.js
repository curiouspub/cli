'use strict';

// Trying again, and the two waits between three attempts.
//
// EVERY COUNT AND EVERY INTERVAL HERE IS TAKEN AT THE SERVER. Counting
// attempts inside the script would let a configuration error that never
// reaches the network look like three attempts, and a backoff asserted
// against a fake clock or a standalone helper proves nothing about the
// downloader that will actually pause.

const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');

const h = require('./helpers/harness');

const V = h.VERSION;
const ASSET = `curious_${V}_linux_amd64.gz`;
const TARGET = { platform: 'linux', arch: 'x64' };

test('a connection that keeps failing is tried three times and then explained', async (t) => {
  // A listener that accepts and drops. The client sees a connection
  // that worked and then did not, which is the ordinary shape of a
  // network fault rather than a refusal.
  const dead = await h.watchPort(t);
  const dir = h.makePackage(t, { checksums: h.checksumsFor([ASSET]) });
  const run = await h.runInstall(t, dir, {
    base: `http://127.0.0.1:${dead.port}`, ...TARGET,
  });

  assert.notStrictEqual(run.code, 0, run.output);
  assert.strictEqual(dead.arrivals.length, 3,
    `the server saw ${dead.arrivals.length} attempts`);
  assert.match(run.output, /3 attempts/);
  assert.match(run.output, /install again/);
  assert.strictEqual(h.installedBinary(dir, 'curious'), null);
  assert.deepStrictEqual(h.leftovers(dir), []);
});

test('the waits between attempts are one second and then two', async (t) => {
  const dead = await h.watchPort(t);
  const dir = h.makePackage(t, { checksums: h.checksumsFor([ASSET]) });
  const run = await h.runInstall(t, dir, {
    base: `http://127.0.0.1:${dead.port}`, ...TARGET,
  });
  assert.notStrictEqual(run.code, 0, run.output);
  assert.strictEqual(dead.arrivals.length, 3);

  // THREE ATTEMPTS HAVE TWO GAPS. A schedule with a third term is
  // unsatisfiable without quietly adding a fourth attempt, which is
  // exactly the shape a row can force an implementer into.
  const gaps = [dead.arrivals[1] - dead.arrivals[0], dead.arrivals[2] - dead.arrivals[1]];
  assert.ok(gaps[0] >= 900 && gaps[0] < 1800, `first wait was ${gaps[0]}ms, want about 1000`);
  assert.ok(gaps[1] >= 1900 && gaps[1] < 2800, `second wait was ${gaps[1]}ms, want about 2000`);
});

test('a download that is slow but still moving is not killed', async (t) => {
  // Six chunks a quarter of a second apart: a transfer that takes far
  // longer than any per-chunk patience and is progressing the whole
  // time.
  const server = await h.serveAssets(t, {
    handler: (record, res) => {
      res.writeHead(200, { 'content-length': String(h.BINARY_GZ.length) });
      const size = Math.ceil(h.BINARY_GZ.length / 6);
      let offset = 0;
      const push = () => {
        if (offset >= h.BINARY_GZ.length) {
          res.end();
          return;
        }
        res.write(h.BINARY_GZ.subarray(offset, offset + size));
        offset += size;
        setTimeout(push, 250);
      };
      push();
    },
  });
  const dir = h.makePackage(t, { checksums: h.checksumsFor([ASSET]) });
  const run = await h.runInstall(t, dir, { base: server.origin, ...TARGET });

  assert.strictEqual(run.code, 0, run.output);
  assert.strictEqual(server.requests.length, 1, 'a progressing transfer was abandoned and retried');
  assert.ok(run.ms >= 1200, `the transfer finished in ${run.ms}ms, so it was not the slow one`);
  assert.deepStrictEqual(h.installedBinary(dir, 'curious'), h.BINARY_BODY);
});

test('a server fault is tried again and a missing file is not', async (t) => {
  const faulty = await h.serveAssets(t, {
    handler: (record, res) => res.writeHead(503).end(),
  });
  const missing = await h.serveAssets(t, {
    handler: (record, res) => res.writeHead(404).end(),
  });

  const first = h.makePackage(t, { checksums: h.checksumsFor([ASSET]) });
  const faultyRun = await h.runInstall(t, first, { base: faulty.origin, ...TARGET });
  assert.notStrictEqual(faultyRun.code, 0, faultyRun.output);
  assert.strictEqual(faulty.requests.length, 3);

  const second = h.makePackage(t, { checksums: h.checksumsFor([ASSET]) });
  const missingRun = await h.runInstall(t, second, { base: missing.origin, ...TARGET });
  assert.notStrictEqual(missingRun.code, 0, missingRun.output);
  // ONE. An answer that will be the same in a second is not worth
  // three seconds of waiting to hear again.
  assert.strictEqual(missing.requests.length, 1);
  assert.match(missingRun.output, /404/);
});

// The same two numbers as the redirect row's, chosen again here because
// they bound a different thing: how long this row will wait to catch a
// client still reading a fault it has already given up on, and how often
// to feed the stream while it waits.
const ENDLESS_BODY_MS = 2_000;
const CHUNK_EVERY_MS = 5;

test('a fault whose body never ends is closed on every attempt', async (t) => {
  // A server fault is retried, so this is three responses rather than
  // one — and a client that only drained them would be holding three
  // open at once by the end.
  const bodies = [];
  const faulty = await h.serveAssets(t, {
    handler: (record, res) => {
      res.writeHead(503);
      bodies.push(h.keepSending(res, { forMs: ENDLESS_BODY_MS, everyMs: CHUNK_EVERY_MS }));
    },
  });

  const dir = h.makePackage(t, { checksums: h.checksumsFor([ASSET]) });
  const run = await h.runInstall(t, dir, { base: faulty.origin, ...TARGET });

  // THE PRESENCE HALF: it really did ask three times and really did
  // stop, with the message a person is meant to read.
  assert.notStrictEqual(run.code, 0, run.output);
  assert.strictEqual(faulty.requests.length, 3);
  assert.match(run.output, /3 attempts/);
  assert.deepStrictEqual(h.leftovers(dir), []);

  // AND THE ABSENCE: every one of them was let go of.
  assert.strictEqual(bodies.length, 3);
  assert.deepStrictEqual(bodies.map((b) => b.hungUp), [true, true, true],
    `chunks written after each fault: ${bodies.map((b) => b.chunks).join(', ')}`);
});

test('the numbers this script chose are the numbers it declares', () => {
  // A PIN, and it is named as one rather than dressed as a behaviour
  // row. No row here can observe thirty seconds without spending
  // thirty, and no row can show that a retry gets a FRESH deadline
  // without two attempts that each outlast one. What this buys is that
  // the numbers cannot be changed silently; the rows above are what
  // show the two waits and the three attempts really happen.
  const source = fs.readFileSync(path.join(h.PACKAGE_ROOT, 'install.js'), 'utf8');
  assert.match(source, /const ATTEMPT_TIMEOUT_MS = 30_000;/);
  assert.match(source, /const ATTEMPTS = 3;/);
  assert.match(source, /const BACKOFF_MS = \[1_000, 2_000\];/);
  assert.match(source, /const MAX_REDIRECTS = 5;/);
});
