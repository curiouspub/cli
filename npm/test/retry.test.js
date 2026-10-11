'use strict';

// Trying again, and the two waits between three attempts.
//
// THE COUNT OF ATTEMPTS AND THE PAUSES BETWEEN THEM ARE TAKEN AT THE
// SERVER. Counting attempts inside the script would let a configuration
// error that never reaches the network look like three attempts, and a
// backoff asserted against a fake clock or a standalone helper proves
// nothing about the downloader that will actually pause.
//
// WHICH WAITS THE SCRIPT CHOSE IS TAKEN FROM THE CHILD. A preload writes
// down every delay the running script asks the timer for, and when each
// one fires, and hands every one to the real timer. A stopwatch cannot
// carry that half: a shared runner can stall a process for seconds, and
// the pause the server sees then measures the machine, not the schedule.
// So the server's pauses are bounded from below only, which load cannot
// break, and the record says what the schedule is.

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
  const record = path.join(h.tempDir(t, 'waits'), 'waits.txt');
  const run = await h.runInstall(t, dir, {
    base: `http://127.0.0.1:${dead.port}`, ...TARGET,
    env: { CURIOUS_TEST_WAITS_FILE: record },
    preload: [h.RECORDED_WAITS],
  });

  const asked = fs.existsSync(record) ? fs.readFileSync(record, 'utf8').trim().split('\n') : [];
  // THREE ATTEMPTS HAVE TWO GAPS. A schedule with a third term is
  // unsatisfiable without quietly adding a fourth attempt, which is
  // exactly the shape a row can force an implementer into.
  const gaps = [dead.arrivals[1] - dead.arrivals[0], dead.arrivals[2] - dead.arrivals[1]];
  const shown = gaps.map((g) => `${Math.round(g)}ms`);
  // Printed BEFORE anything is asserted, on every run, so a green log is a
  // sample too, and a red one still says what it saw.
  t.diagnostic(`asked for: ${asked.join(', ')}; pauses at the server: ${shown.join(', ')}`);

  await t.test('the server saw three attempts', () => {
    assert.notStrictEqual(run.code, 0, run.output);
    assert.strictEqual(dead.arrivals.length, 3, `the server saw ${dead.arrivals.length} attempts`);
  });

  // Each attempt arms its own thirty-second deadline, then the pause is
  // asked for, and the pause FIRES before the next attempt arms anything.
  // A pause that is asked for and not waited on puts the next deadline
  // ahead of its firing.
  await t.test('the script waited one second and then two', () => {
    assert.deepStrictEqual(asked, [
      'set 30000',
      'set 1000',
      'fired 1000',
      'set 30000',
      'set 2000',
      'fired 2000',
      'set 30000',
    ]);
  });

  // NO CEILING. The server stamps an arrival and drops it in one step, so
  // the script cannot start a pause before its stamp, and a stalled
  // machine can only lengthen what is measured here. What a floor catches
  // is a pause that was recorded and never really spent.
  await t.test('each pause really passed', () => {
    assert.ok(gaps[0] >= 900, `first pause was ${shown[0]}, want at least 900ms`);
    assert.ok(gaps[1] >= 1900, `second pause was ${shown[1]}, want at least 1900ms`);
  });
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
  // thirty. The waits row shows that each attempt ARMS a fresh deadline
  // of that length; it does not show the deadline bounding or ending an
  // attempt, which would take two attempts that each outlast one. What
  // this buys is that the numbers cannot be changed silently; the rows
  // above are what show the two waits and the three attempts really
  // happen.
  const source = fs.readFileSync(path.join(h.PACKAGE_ROOT, 'install.js'), 'utf8');
  assert.match(source, /const ATTEMPT_TIMEOUT_MS = 30_000;/);
  assert.match(source, /const ATTEMPTS = 3;/);
  assert.match(source, /const BACKOFF_MS = \[1_000, 2_000\];/);
  assert.match(source, /const MAX_REDIRECTS = 5;/);
});
