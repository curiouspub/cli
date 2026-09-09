'use strict';

// The step that turns the release's own checksum file into the table
// this package ships.
//
// IT IS THE ONLY THING ENFORCING THE INVARIANT THE WHOLE DOWNLOAD
// ADDRESS RESTS ON: the package's version, the tag, and every key in
// the table are the same version in three spellings, and nothing else
// in the pipeline compares them.

const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');
const { execFile } = require('node:child_process');

const h = require('./helpers/harness');

const TOOL = path.join(h.PACKAGE_ROOT, 'tools', 'build-checksums.js');
const V = h.VERSION;

const PLATFORMS = [
  ['darwin', 'amd64'], ['darwin', 'arm64'],
  ['linux', 'amd64'], ['linux', 'arm64'],
  ['windows', 'amd64'], ['windows', 'arm64'],
];

function digestFor(name) {
  // A fixed, recognisable digest per name. Nothing here recomputes a
  // hash: the tool's job is to copy digests across, not to make them.
  return name.length.toString(16).padStart(2, '0').repeat(32);
}

// A checksum file shaped like the release tool's own: every archive,
// both formats, in the order it happens to emit them.
function releaseChecksums({ omit = null, digests = digestFor } = {}) {
  const lines = [];
  for (const [os, arch] of PLATFORMS) {
    const archive = os === 'windows'
      ? `curious_${V}_${os}_${arch}.zip`
      : `curious_${V}_${os}_${arch}.tar.gz`;
    lines.push(`${digests(archive)}  ${archive}`);
    const bare = `curious_${V}_${os}_${arch}.gz`;
    if (bare !== omit) {
      lines.push(`${digests(bare)}  ${bare}`);
    }
  }
  return lines.join('\n') + '\n';
}

function runTool(t, args) {
  return new Promise((resolve) => {
    execFile(process.execPath, [TOOL, ...args], { encoding: 'utf8' },
      (err, stdout, stderr) => {
        resolve({ code: err ? (err.code ?? 1) : 0, output: stdout + stderr });
      });
  });
}

test('the table it writes covers every supported platform and nothing else', async (t) => {
  const dir = h.tempDir(t, 'checksums');
  const input = path.join(dir, 'checksums.txt');
  const output = path.join(dir, 'checksums.json');
  fs.writeFileSync(input, releaseChecksums());

  const run = await runTool(t, ['--tag', `v${V}`, '--checksums', input, '--out', output]);
  assert.strictEqual(run.code, 0, run.output);

  const table = JSON.parse(fs.readFileSync(output, 'utf8'));
  assert.deepStrictEqual(Object.keys(table).sort(),
    PLATFORMS.map(([os, arch]) => `curious_${V}_${os}_${arch}.gz`).sort());
  for (const [name, digest] of Object.entries(table)) {
    assert.strictEqual(digest, digestFor(name), `${name} carries the wrong digest`);
    // EVERY KEY CARRIES THE SAME VERSION. The address the install
    // script builds puts the version in twice, in two spellings, and
    // nothing else checks that they agree.
    assert.ok(name.includes(`_${V}_`), `${name} is not this package's version`);
  }
});

test('a tag that is not this package\'s version is refused', async (t) => {
  const dir = h.tempDir(t, 'checksums');
  const input = path.join(dir, 'checksums.txt');
  fs.writeFileSync(input, releaseChecksums());
  const run = await runTool(t, [
    '--tag', 'v9.9.9', '--checksums', input, '--out', path.join(dir, 'out.json')]);
  assert.notStrictEqual(run.code, 0, run.output);
  assert.match(run.output, /9\.9\.9/);
  assert.match(run.output, new RegExp(V.replace(/\./g, '\\.')));
  assert.ok(!fs.existsSync(path.join(dir, 'out.json')), 'it wrote a table it had refused');
});

test('a platform missing from the release is named rather than skipped', async (t) => {
  const dir = h.tempDir(t, 'checksums');
  const input = path.join(dir, 'checksums.txt');
  const missing = `curious_${V}_windows_arm64.gz`;
  fs.writeFileSync(input, releaseChecksums({ omit: missing }));

  const run = await runTool(t, [
    '--tag', `v${V}`, '--checksums', input, '--out', path.join(dir, 'out.json')]);
  assert.notStrictEqual(run.code, 0, run.output);
  assert.match(run.output, new RegExp(missing.replace(/\./g, '\\.')));
  // A table with five of six entries installs on five platforms and
  // refuses on the sixth, in the field, after publication.
  assert.ok(!fs.existsSync(path.join(dir, 'out.json')));
});

test('a digest that is not one is refused', async (t) => {
  const dir = h.tempDir(t, 'checksums');
  const input = path.join(dir, 'checksums.txt');
  fs.writeFileSync(input, releaseChecksums({ digests: () => 'not-a-digest' }));
  const run = await runTool(t, [
    '--tag', `v${V}`, '--checksums', input, '--out', path.join(dir, 'out.json')]);
  assert.notStrictEqual(run.code, 0, run.output);
  assert.match(run.output, /not-a-digest/);
});

test('the table it writes is one the install script accepts', async (t) => {
  // THE JOIN. The tool and the script agree about the shape of this
  // file, and the only way to know that is to hand one's output to the
  // other. Everything above tests the tool against a description.
  const work = h.tempDir(t, 'join');
  const input = path.join(work, 'checksums.txt');
  const output = path.join(work, 'checksums.json');
  const asset = `curious_${V}_linux_amd64.gz`;
  fs.writeFileSync(input, releaseChecksums({
    digests: (name) => (name === asset ? h.BINARY_DIGEST : digestFor(name)),
  }));
  const built = await runTool(t, ['--tag', `v${V}`, '--checksums', input, '--out', output]);
  assert.strictEqual(built.code, 0, built.output);

  const server = await h.serveAssets(t);
  const dir = h.makePackage(t, { checksums: JSON.parse(fs.readFileSync(output, 'utf8')) });
  const run = await h.runInstall(t, dir, {
    base: server.origin, platform: 'linux', arch: 'x64',
  });
  assert.strictEqual(run.code, 0, run.output);
  assert.deepStrictEqual(h.installedBinary(dir, 'curious'), h.BINARY_BODY);
});
