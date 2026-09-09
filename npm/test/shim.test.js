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
function installedPackage(t, { body = null, marker = true, markerVersion = h.VERSION } = {}) {
  const dir = h.makePackage(t);
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

test('a missing binary is explained rather than thrown', async (t) => {
  const dir = installedPackage(t, { body: null, marker: false });
  const run = await runShim(dir);
  assert.notStrictEqual(run.code, 0);
  assert.match(run.output, /--ignore-scripts/);
  assert.match(run.output, /postinstall/);
  // A stack trace is what this row exists to prevent: it tells the
  // reader about this file rather than about what they should do.
  assert.ok(!/ {4}at /.test(run.output), `the shim printed a stack trace:\n${run.output}`);
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
