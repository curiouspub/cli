'use strict';

// What the tarball actually contains, and what happens to somebody who
// installs it with the scripts turned off.
//
// THESE ROWS RUN THE REAL PACKAGING TOOL. A manifest can list a file
// that is not there, an allowlist can let a local artefact through, and
// a bin entry can point at nothing — none of which a row reading
// package.json could see.

const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');
const { execFile } = require('node:child_process');

const h = require('./helpers/harness');

function run(command, args, options = {}) {
  return new Promise((resolve) => {
    execFile(command, args, { encoding: 'utf8', ...options }, (err, stdout, stderr) => {
      resolve({ code: err ? (err.code ?? 1) : 0, stdout, stderr, output: stdout + stderr });
    });
  });
}

// HOW A TEST PROCESS RUNS npm, AND WHY IT IS NOT `npm`.
//
// There is no extensionless executable called npm on Windows: the
// launcher is npm.cmd, and a .cmd file cannot be spawned without a
// shell. The platform refuses it, and what reaches the caller is
// `spawn npm ENOENT` — errno -4058, path "npm", printed from the
// Windows leg itself rather than guessed here. A shell would run it,
// and would also hand every argument to a command interpreter, which is
// the thing that refusal exists to prevent. So these rows run npm the
// way npm's own launcher does: this node, on npm's entry script.
//
// AND NOT FINDING ONE IS AN ERROR, never a skip. A packaging row that
// quietly does not run reports that the tarball is fine because nobody
// looked inside it.
function npmCli() {
  const beside = path.dirname(process.execPath);
  const named = [
    process.env.npm_execpath,
    // A node that ships npm beside it, which is every official build:
    // Windows keeps it here, POSIX one level up under lib.
    path.join(beside, 'node_modules', 'npm', 'bin', 'npm-cli.js'),
    path.join(beside, '..', 'lib', 'node_modules', 'npm', 'bin', 'npm-cli.js'),
  ];
  for (const candidate of named) {
    if (candidate && candidate.endsWith('.js') && fs.existsSync(candidate)) {
      return candidate;
    }
  }
  // A package manager installed on its own rather than beside node. The
  // launcher on PATH is a link to the entry script wherever links
  // exist — which is not Windows, where the branch above has answered.
  for (const entry of (process.env.PATH || '').split(path.delimiter)) {
    try {
      const real = fs.realpathSync(path.join(entry, 'npm'));
      if (real.endsWith('.js')) {
        return real;
      }
    } catch {
      // Not on this entry.
    }
  }
  throw new Error('no npm-cli.js was found for this node, so the packaging rows cannot run');
}

const NPM = npmCli();

function npm(args, options = {}) {
  return run(process.execPath, [NPM, ...args], options);
}

// What the REPOSITORY records, which is the same answer on every
// platform because it is not a question about a filesystem.
async function gitMode(repoPath) {
  const shown = await run('git', ['ls-files', '-s', '--', repoPath], { cwd: h.PACKAGE_ROOT });
  assert.strictEqual(shown.code, 0, shown.output);
  return shown.stdout.split(/\s+/)[0];
}

const manifest = JSON.parse(
  fs.readFileSync(path.join(h.PACKAGE_ROOT, 'package.json'), 'utf8'));

test('the manifest carries what a provenance publish needs', () => {
  // Provenance is keyed to the repository field. Without it the publish
  // fails rather than quietly producing an unattested package, which is
  // the worse of the two outcomes and the one nobody notices.
  assert.strictEqual(manifest.repository.type, 'git');
  assert.strictEqual(manifest.repository.url, 'https://github.com/curiouspub/cli');
});

test('there is exactly one command, so the run-without-installing form resolves', () => {
  // THE GUARD IS STRICTER THAN THE PROPERTY IT PROTECTS, and that is
  // deliberate. The property is that `npx curiouspub` resolves to
  // exactly one command; a package's runner takes a single bin entry
  // whatever it is called, prefers one named after the package when
  // there are several, and errors when neither applies. So a second
  // entry named curiouspub would resolve perfectly well — one key is
  // simply the sufficient condition that needs no reasoning at the call
  // site.
  assert.deepStrictEqual(Object.keys(manifest.bin), ['curious']);
});

test('the module system is stated rather than inherited', () => {
  // Both shipped scripts resolve paths relative to themselves, and the
  // directory of the current module does not exist under the other
  // module system. Stated so it is a decision.
  assert.strictEqual(manifest.type, 'commonjs');
});

test('the only script that runs during an install is the postinstall', () => {
  // Every name a package manager will run on its own during an install.
  // The manifest may carry others — the suite runner below is one —
  // but none of these.
  const lifecycle = ['preinstall', 'install', 'prepare', 'prepack',
    'prepublish', 'prepublishOnly', 'postpack', 'preuninstall', 'uninstall'];
  for (const name of lifecycle) {
    assert.ok(!(name in manifest.scripts), `${name} runs on somebody else's machine`);
  }
  assert.strictEqual(manifest.scripts.postinstall, 'node install.js');
});

test('the tarball carries the intended files and nothing else', async () => {
  const packed = await npm(['pack', '--dry-run', '--json'], { cwd: h.PACKAGE_ROOT });
  assert.strictEqual(packed.code, 0, packed.output);
  const [report] = JSON.parse(packed.stdout);
  const paths = report.files.map((f) => f.path).sort();

  assert.deepStrictEqual(paths, [
    'LICENSE',
    'README.md',
    'bin/curious.js',
    'checksums.json',
    'install.js',
    'lib/platform.js',
    // The set of hosts this project will speak plaintext to, shipped
    // because the install script reads it at run time.
    'loopback-hosts.json',
    // Always included by the packaging tool, whatever the allowlist
    // says.
    'package.json',
  ]);

  // A bin key pointing at nothing passes a manifest row on its own.
  const shim = report.files.find((f) => f.path === 'bin/curious.js');
  assert.ok(shim, 'the command this package installs is not in the tarball');
  // 0o755 — AND THE BIT IS A PROPERTY OF THE CHECKOUT, not of this
  // package. One platform in this matrix has no such bit: a Windows
  // checkout stores the shim 0o644 and the packaging tool packs what it
  // finds, so one repository produces two different tarballs and only
  // the Linux one is ever published. Measured on the leg, which
  // reported mode 420 where Linux reports 493 — and reported it only
  // once the spawn above stopped failing first, which is why this
  // assertion had never run there.
  //
  // The claim the shim actually depends on is the repository's record,
  // and every platform can read that. The packed mode is then asserted
  // on both sides: the executable one where the filesystem can carry
  // it, and the one Windows must produce where it cannot.
  assert.strictEqual(await gitMode('bin/curious.js'), '100755',
    'the repository does not record the shim as executable');
  if (process.platform === 'win32') {
    assert.strictEqual(shim.mode, 0o644,
      `the shim packed as ${shim.mode.toString(8)} on a filesystem with no executable bit`);
  } else {
    assert.strictEqual(shim.mode, 0o755, `the shim ships as ${shim.mode.toString(8)}`);
  }
});

test('installing with the scripts turned off explains itself', async (t) => {
  const work = h.tempDir(t, 'prefix');
  const packed = await npm(['pack', '--pack-destination', work], { cwd: h.PACKAGE_ROOT });
  assert.strictEqual(packed.code, 0, packed.output);
  const tarball = path.join(work, packed.stdout.trim().split('\n').pop());

  const prefix = path.join(work, 'prefix');
  fs.mkdirSync(prefix);
  const installed = await npm(['install', '--prefix', prefix, '--ignore-scripts',
    '--no-audit', '--no-fund', tarball], { cwd: work });
  assert.strictEqual(installed.code, 0, installed.output);

  const command = path.join(prefix, 'node_modules', '.bin',
    process.platform === 'win32' ? 'curious.cmd' : 'curious');
  assert.ok(fs.existsSync(command), `the package installed no command at ${command}`);

  const ran = await run(command, ['version'], { shell: process.platform === 'win32' });
  assert.notStrictEqual(ran.code, 0, ran.output);
  assert.match(ran.output, /--ignore-scripts/);
  assert.match(ran.output, /postinstall/);
  assert.ok(!/ {4}at /.test(ran.output), `a stack trace reached the user:\n${ran.output}`);
});
