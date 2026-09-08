'use strict';

// The check that runs after a release build and compares what was
// produced against what the wrapper will ask for.
//
// IT IS DRIVEN AGAINST DIRECTORIES ON DISK, because that is what it
// reads. The release tool is not installed on most machines that run
// this suite, so the rows build the output it would have produced —
// including the archive for a person, which shares the directory and
// must not be counted.

const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');
const { execFile } = require('node:child_process');

const h = require('./helpers/harness');

const CHECK = path.join(h.PACKAGE_ROOT, '..', 'scripts', 'check-release-assets.js');

const PLATFORMS = [
  ['darwin', 'amd64'], ['darwin', 'arm64'],
  ['linux', 'amd64'], ['linux', 'arm64'],
  ['windows', 'amd64'], ['windows', 'arm64'],
];

function buildDist(t, version, { omit = null, extra = [] } = {}) {
  const dist = h.tempDir(t, 'dist');
  fs.writeFileSync(path.join(dist, 'metadata.json'), JSON.stringify({ version }));
  fs.writeFileSync(path.join(dist, 'checksums.txt'), '');
  for (const [os, arch] of PLATFORMS) {
    // The archive a person downloads, which shares the directory.
    const human = os === 'windows'
      ? `curious_${version}_${os}_${arch}.zip`
      : `curious_${version}_${os}_${arch}.tar.gz`;
    fs.writeFileSync(path.join(dist, human), '');
    const bare = `curious_${version}_${os}_${arch}.gz`;
    if (bare !== omit) {
      fs.writeFileSync(path.join(dist, bare), '');
    }
  }
  for (const name of extra) {
    fs.writeFileSync(path.join(dist, name), '');
  }
  return dist;
}

function runCheck(dist) {
  return new Promise((resolve) => {
    execFile(process.execPath, [CHECK, dist], { encoding: 'utf8' },
      (err, stdout, stderr) => {
        resolve({ code: err ? (err.code ?? 1) : 0, output: stdout + stderr });
      });
  });
}

test('a complete build passes, and the human archive is not counted', async (t) => {
  const dist = buildDist(t, '1.2.3');
  const run = await runCheck(dist);
  assert.strictEqual(run.code, 0, run.output);
  assert.match(run.output, /6 archives for 1\.2\.3/);
});

test('a platform the release did not build is named', async (t) => {
  const dist = buildDist(t, '1.2.3', { omit: 'curious_1.2.3_windows_arm64.gz' });
  const run = await runCheck(dist);
  assert.notStrictEqual(run.code, 0, run.output);
  assert.match(run.output, /curious_1\.2\.3_windows_arm64\.gz/);
});

test('an archive under a name the wrapper never asks for is refused', async (t) => {
  // This is the shape a template change produces: six archives built,
  // six expected, and not one of the names in common.
  const dist = buildDist(t, '1.2.3', { extra: ['curious_Darwin_x86_64.gz'] });
  const run = await runCheck(dist);
  assert.notStrictEqual(run.code, 0, run.output);
  assert.match(run.output, /curious_Darwin_x86_64\.gz/);
});

test('a build with no single-member archives at all is refused', async (t) => {
  const dist = h.tempDir(t, 'dist');
  fs.writeFileSync(path.join(dist, 'metadata.json'), JSON.stringify({ version: '1.2.3' }));
  fs.writeFileSync(path.join(dist, 'curious_1.2.3_linux_amd64.tar.gz'), '');
  const run = await runCheck(dist);
  assert.notStrictEqual(run.code, 0, run.output);
  assert.match(run.output, /built 0 single-member archives/);
});

test('no build at all is a failure rather than a pass over nothing', async (t) => {
  const empty = h.tempDir(t, 'dist');
  const run = await runCheck(empty);
  assert.notStrictEqual(run.code, 0, run.output);
  assert.match(run.output, /metadata\.json/);
});
