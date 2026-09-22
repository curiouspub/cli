'use strict';

// The bin shim: the thing `curious` actually is once the package is
// installed. It finds the downloaded binary, runs it, and gets out of
// the way.
//
// EVERY ROW RUNS THE REAL SHIM AGAINST A REAL CHILD. A stub that prints
// what it was given is the only thing that can show argument
// forwarding: running `curious version` proves the child ran, not that
// it was told anything.

const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');

const h = require('./helpers/harness');

const POSIX_ONLY = process.platform === 'win32'
  ? 'the shim spawns without a command interpreter, and Windows has no ' +
    'stub it can execute that this suite could write'
  : false;

// A package directory with a stub standing in for the downloaded
// binary, and the marker a finished install would have left.
// Every asset name the release publishes, so a row can hand the package
// a complete checksums table without working out which platform it is
// running on — the mapping itself is install.test.js's subject.
const ALL_ASSET_NAMES = [
  ['darwin', 'amd64'], ['darwin', 'arm64'],
  ['linux', 'amd64'], ['linux', 'arm64'],
  ['windows', 'amd64'], ['windows', 'arm64'],
].map(([osName, arch]) => `curious_${h.VERSION}_${osName}_${arch}.gz`);

function installedPackage(t, {
  body = null, marker = true, markerVersion = h.VERSION, checksums = {},
} = {}) {
  const dir = h.makePackage(t, { checksums });
  if (body !== null) {
    const binary = path.join(dir, process.platform === 'win32' ? 'curious.exe' : 'curious');
    fs.writeFileSync(binary, body);
    fs.chmodSync(binary, 0o755);
  }
  if (marker) {
    fs.writeFileSync(path.join(dir, 'installed.json'),
      JSON.stringify({ version: markerVersion, binary: 'curious' }));
  }
  return dir;
}

function runShim(dir, args = [], options = {}) {
  const child = spawn(process.execPath, [path.join(dir, 'bin', 'curious.js'), ...args], {
    cwd: dir,
    env: { ...process.env, ...(options.env || {}) },
  });
  let stdout = '';
  let stderr = '';
  child.stdout.on('data', (d) => { stdout += d; });
  child.stderr.on('data', (d) => { stderr += d; });
  return new Promise((resolve) => {
    child.on('close', (code, signal) => {
      resolve({ code, signal, stdout, stderr, output: stdout + stderr });
    });
  });
}

test('the shim forwards its arguments exactly', { skip: POSIX_ONLY }, async (t) => {
  const dir = installedPackage(t, { body: '#!/bin/sh\nprintf "%s\\n" "$@"\n' });
  const args = ['deploy', './some dir', '--flag=a b', "it's"];
  const run = await runShim(dir, args);
  assert.strictEqual(run.code, 0, run.output);
  assert.deepStrictEqual(run.stdout.split('\n').slice(0, args.length), args);
});

test('the shim exits with the code the binary exited with', { skip: POSIX_ONLY }, async (t) => {
  const dir = installedPackage(t, { body: '#!/bin/sh\nexit 3\n' });
  const run = await runShim(dir);
  assert.strictEqual(run.code, 3);
});

test('a binary killed by a signal is reported the way a shell reports it',
  { skip: POSIX_ONLY }, async (t) => {
    for (const [name, number] of [['TERM', os.constants.signals.SIGTERM],
      ['INT', os.constants.signals.SIGINT]]) {
      const dir = installedPackage(t, { body: `#!/bin/sh\nkill -${name} $$\n` });
      const run = await runShim(dir);
      assert.strictEqual(run.code, 128 + number,
        `a child killed by SIG${name} made the shim exit ${run.code}`);
    }
  });

// THIS ROW USED TO ASSERT A REFUSAL, and what it asserts now is the
// change: a missing binary is FETCHED rather than explained away.
//
// The old message told the reader their install had run with
// --ignore-scripts and to do it again without. That was accurate and
// useless on two of the three paths this tool is installed by: a global
// install and npx have no project package.json, so there is nowhere for
// the approval a blocking package manager will want to live.
test('a missing binary is fetched rather than refused', async (t) => {
  const server = await h.serveAssets(t);
  const dir = installedPackage(t, {
    body: null,
    marker: false,
    checksums: h.checksumsFor(ALL_ASSET_NAMES),
  });

  const run = await runShim(dir, ['--version'], {
    env: { CURIOUS_RELEASE_BASE_URL: server.origin },
  });

  assert.strictEqual(run.code, 0, `the shim did not fetch and run:\n${run.output}`);
  assert.ok(server.requests.length > 0, 'the shim ran without asking the release for anything');
  // THE PROGRESS LINE IS ON STDERR, because `curious version` is a
  // command whose output somebody pipes into a file.
  assert.match(run.stderr, /fetching the binary/);
  assert.ok(!/fetching the binary/.test(run.stdout),
    `the progress line reached stdout, where a redirect would capture it:\n${run.stdout}`);
});

test('a second run does not fetch again', async (t) => {
  const server = await h.serveAssets(t);
  const dir = installedPackage(t, {
    body: null,
    marker: false,
    checksums: h.checksumsFor(ALL_ASSET_NAMES),
  });
  const env = { CURIOUS_RELEASE_BASE_URL: server.origin };

  const first = await runShim(dir, ['--version'], { env });
  assert.strictEqual(first.code, 0, first.output);
  const afterFirst = server.requests.length;

  const second = await runShim(dir, ['--version'], { env });
  assert.strictEqual(second.code, 0, second.output);

  assert.strictEqual(server.requests.length, afterFirst,
    'the second run asked the release for something again — the marker written by the ' +
    'first run is what stops every invocation paying for a download');
  assert.ok(!/fetching the binary/.test(second.output),
    `the second run announced a fetch it did not make:\n${second.output}`);
});

test('a fetch that fails is explained rather than thrown', async (t) => {
  // A package whose checksums name the asset, pointed at a server that
  // does not have it: the fetch is attempted and fails.
  const server = await h.serveAssets(t, {
    handler: (record, res) => {
      res.writeHead(404, { 'content-type': 'text/plain' });
      res.end('no such asset\n');
    },
  });
  const dir = installedPackage(t, {
    body: null,
    marker: false,
    checksums: h.checksumsFor(ALL_ASSET_NAMES),
  });

  const run = await runShim(dir, ['--version'], {
    env: { CURIOUS_RELEASE_BASE_URL: server.origin },
  });

  assert.notStrictEqual(run.code, 0, `a failed fetch reported success:\n${run.output}`);
  // A stack trace is what this row exists to prevent: it tells the
  // reader about this file rather than about what they should do.
  assert.ok(!/ {4}at /.test(run.output), `the shim printed a stack trace:\n${run.output}`);
  assert.ok(run.output.trim().length > 0, 'the shim failed silently');
});

test('a binary left by a different version is not run', { skip: POSIX_ONLY }, async (t) => {
  const dir = installedPackage(t, {
    body: '#!/bin/sh\necho "the previous release"\n',
    markerVersion: '0.0.1-previous',
  });
  const run = await runShim(dir);
  assert.notStrictEqual(run.code, 0, run.output);
  assert.ok(!run.stdout.includes('the previous release'),
    'the shim ran a binary from another version');
  assert.match(run.output, /install/);

  // THE POSITIVE CONTROL, and it is what makes the row above mean
  // anything: the same shim, the same binary, a marker that agrees,
  // and it runs.
  const current = installedPackage(t, { body: '#!/bin/sh\necho "this release"\n' });
  const ok = await runShim(current);
  assert.strictEqual(ok.code, 0, ok.output);
  assert.match(ok.stdout, /this release/);
});

test('the shim does not re-quote arguments through a command interpreter', () => {
  // A pin on the spawn options, because the failure it prevents is
  // invisible on the platform most people develop on: `shell: true`
  // would re-parse every argument, and a project directory with a
  // space in its name would arrive as two.
  const source = fs.readFileSync(path.join(h.PACKAGE_ROOT, 'bin', 'curious.js'), 'utf8');
  assert.match(source, /shell:\s*false/);
  assert.match(source, /stdio:\s*'inherit'/);
  // Signals are deliberately NOT forwarded: on a POSIX terminal the
  // child is already in the foreground process group and would be
  // interrupted twice, and on Windows the signal argument to kill is
  // ignored and the process is destroyed instead.
  assert.ok(!/\.kill\(/.test(source), 'the shim forwards signals; both platforms already do this');
});
