'use strict';

// The postinstall, run for real against a server that records what it
// was asked for.
//
// EVERY ROW HERE WATCHES THE WIRE OR THE DISK. Where a row can only see
// the source — "this word does not appear in the file" — it is paired
// with one that sees a request or a file, because a mapping table can be
// right while the downloader asks for something else.

const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');
const zlib = require('node:zlib');

const h = require('./helpers/harness');

const V = h.VERSION;

// The six names the release publishes, written out. Nothing derives
// them from the mapping the script uses.
const ASSETS = [
  ['darwin', 'x64', `curious_${V}_darwin_amd64.gz`],
  ['darwin', 'arm64', `curious_${V}_darwin_arm64.gz`],
  ['linux', 'x64', `curious_${V}_linux_amd64.gz`],
  ['linux', 'arm64', `curious_${V}_linux_arm64.gz`],
  ['win32', 'x64', `curious_${V}_windows_amd64.gz`],
  ['win32', 'arm64', `curious_${V}_windows_arm64.gz`],
];
const ALL_NAMES = ASSETS.map(([, , name]) => name);

test('the asset it asks for is the one the release publishes, for every platform', async (t) => {
  const server = await h.serveAssets(t);
  const seen = [];
  for (const [platform, arch] of ASSETS) {
    const dir = h.makePackage(t, { checksums: h.checksumsFor(ALL_NAMES) });
    const run = await h.runInstall(t, dir, { base: server.origin, platform, arch });
    assert.strictEqual(run.code, 0, `${platform}/${arch} did not install:\n${run.output}`);
    seen.push(server.requests.at(-1).url);
  }
  // The PATH THE SERVER RECEIVED, for all six. A correct mapping table
  // beside a downloader that asks for something else passes the table
  // row and fails this one.
  assert.deepStrictEqual(seen, ALL_NAMES.map((name) => `/v${V}/${name}`));
});

test('the download URL carries the tag path and the release spelling of the architecture', async (t) => {
  const server = await h.serveAssets(t);
  const dir = h.makePackage(t, { checksums: h.checksumsFor(ALL_NAMES) });
  const run = await h.runInstall(t, dir, {
    base: server.origin, platform: 'linux', arch: 'x64',
  });
  assert.strictEqual(run.code, 0, run.output);
  const asked = server.requests[0].url;
  // The tag keeps its leading v; the asset name does not.
  assert.ok(asked.startsWith(`/v${V}/`), `asked for ${asked}`);
  assert.ok(asked.includes('_amd64.gz'), `asked for ${asked}`);
  assert.ok(!asked.includes('x64'), `asked for ${asked}`);
});

test('nothing in the install script reaches for the newest release', () => {
  // The partner of the row above, and useless without it: this one
  // cannot see a runtime constant, and that one cannot see a fallback
  // never taken.
  const source = fs.readFileSync(path.join(h.PACKAGE_ROOT, 'install.js'), 'utf8');
  assert.ok(!/\blatest\b/.test(source),
    'the install script mentions the newest release; it must only ever fetch its own version');
});

test('an unsupported platform fails the install before any network call', async (t) => {
  const server = await h.serveAssets(t);
  for (const [platform, arch] of [['linux', 'ppc64'], ['aix', 'x64']]) {
    const dir = h.makePackage(t, { checksums: h.checksumsFor(ALL_NAMES) });
    const run = await h.runInstall(t, dir, { base: server.origin, platform, arch });
    assert.notStrictEqual(run.code, 0, `${platform}/${arch} installed and should not have`);
    assert.match(run.output, new RegExp(platform));
    assert.match(run.output, new RegExp(arch));
    for (const [supportedOS, supportedArch] of ASSETS) {
      assert.ok(run.output.includes(`${supportedOS}/${supportedArch}`),
        `the refusal does not list ${supportedOS}/${supportedArch}:\n${run.output}`);
    }
    assert.match(run.output, /releases/);
    assert.match(run.output, /go install/);
    assert.deepStrictEqual(h.leftovers(dir), []);
  }
  // THE PRESENCE HALF: this server answered six successful installs in
  // the row above, so "it saw nothing" here is a refusal rather than a
  // server nobody could have reached.
  assert.strictEqual(server.requests.length, 0);
});

test('a checksum table with no entry for this platform refuses, naming the key it wanted', async (t) => {
  const server = await h.serveAssets(t);
  const dir = h.makePackage(t, {
    // Every other platform, and not this one: a stale generator produces
    // exactly this, and `expected && expected !== actual` installs
    // whatever arrives.
    checksums: h.checksumsFor(ALL_NAMES.filter((n) => !n.includes('linux_amd64'))),
  });
  const run = await h.runInstall(t, dir, {
    base: server.origin, platform: 'linux', arch: 'x64',
  });
  assert.notStrictEqual(run.code, 0, run.output);
  assert.match(run.output, new RegExp(`curious_${V}_linux_amd64\\.gz`));
  assert.strictEqual(server.requests.length, 0,
    'the missing key was found after the download rather than before it');
  assert.deepStrictEqual(h.leftovers(dir), []);
});

test('a tampered payload is refused, and the untampered one installs', async (t) => {
  // ONE BYTE OF THE COMPRESSED STREAM, FLIPPED. The first spelling of
  // this row overwrote the last byte with a zero — which the gzip
  // trailer already held, so the "tampered" payload was the fixture and
  // the row reported a refusal that never happened. A mutation that
  // changes nothing is not a mutation, and it looks exactly like one.
  const TAMPERED = Buffer.from(h.BINARY_GZ);
  TAMPERED[12] ^= 0xff;
  assert.notDeepStrictEqual(TAMPERED, h.BINARY_GZ, 'the tampered payload is the fixture');

  let tamper = true;
  const server = await h.serveAssets(t, {
    handler: (record, res) => {
      const body = tamper ? TAMPERED : h.BINARY_GZ;
      res.writeHead(200, { 'content-length': String(body.length) });
      res.end(body);
    },
  });

  const bad = h.makePackage(t, { checksums: h.checksumsFor(ALL_NAMES) });
  const refused = await h.runInstall(t, bad, {
    base: server.origin, platform: 'linux', arch: 'x64',
  });
  assert.notStrictEqual(refused.code, 0, refused.output);
  // Both digests, so a person can compare them with the release page.
  assert.ok(refused.output.includes(h.BINARY_DIGEST),
    `the expected digest is not in the message:\n${refused.output}`);
  assert.match(refused.output, /[0-9a-f]{64}[\s\S]*[0-9a-f]{64}/);
  // NAMED SPECIFICALLY: the shim lives in bin/ and is always there, so
  // "nothing in bin/" would be ambiguous. This is the downloaded
  // executable itself.
  assert.strictEqual(h.installedBinary(bad, 'curious'), null);
  assert.deepStrictEqual(h.leftovers(bad), []);

  // THE POSITIVE CONTROL. Without it, a script that refused everything
  // would pass the row above.
  tamper = false;
  const good = h.makePackage(t, { checksums: h.checksumsFor(ALL_NAMES) });
  const installed = await h.runInstall(t, good, {
    base: server.origin, platform: 'linux', arch: 'x64',
  });
  assert.strictEqual(installed.code, 0, installed.output);
  assert.deepStrictEqual(h.installedBinary(good, 'curious'), h.BINARY_BODY);
});

test('exactly one request is made, and the file that lands is the gunzipped fixture', async (t) => {
  const server = await h.serveAssets(t);
  const dir = h.makePackage(t, { checksums: h.checksumsFor(ALL_NAMES) });
  const run = await h.runInstall(t, dir, {
    base: server.origin, platform: 'linux', arch: 'x64',
  });
  assert.strictEqual(run.code, 0, run.output);
  assert.strictEqual(server.requests.length, 1);
  assert.strictEqual(server.requests[0].url, `/v${V}/curious_${V}_linux_amd64.gz`);
  // BYTES, not a source scan for the name of a decompressor.
  assert.deepStrictEqual(h.installedBinary(dir, 'curious'), h.BINARY_BODY);
  assert.notDeepStrictEqual(h.installedBinary(dir, 'curious'), h.BINARY_GZ);
  // Nothing compressed is left behind.
  assert.deepStrictEqual(h.leftovers(dir), ['curious', 'installed.json']);
});

test('the installed file is executable and runs', { skip: process.platform === 'win32'
  ? 'a POSIX mode bit and a shell script have no Windows equivalent' : false }, async (t) => {
  const server = await h.serveAssets(t);
  const dir = h.makePackage(t, { checksums: h.checksumsFor(ALL_NAMES) });
  const run = await h.runInstall(t, dir, { base: server.origin });
  assert.strictEqual(run.code, 0, run.output);
  const installed = path.join(dir, 'curious');
  assert.ok(fs.statSync(installed).mode & 0o111, 'the installed binary is not executable');
  const { execFileSync } = require('node:child_process');
  assert.match(execFileSync(installed, { encoding: 'utf8' }), /curious fixture ok/);
});

test('the script contains no archive parser', () => {
  // The companion of the bytes row above. A later "Windows fix" that
  // reintroduced a tar or zip reader would keep the bytes row green by
  // handling gzip too, and this one reds.
  const source = fs.readFileSync(path.join(h.PACKAGE_ROOT, 'install.js'), 'utf8');
  for (const parser of [/\btar\b/i, /\bzip\b(?!(\.|_)?(gz|sync))/i, /\bunzip\b/i, /\badm-zip\b/i]) {
    const match = source.match(parser);
    assert.strictEqual(match, null,
      `the install script mentions ${match && match[0]}; the asset is a single-member gzip ` +
      'and the standard library reads no archive format');
  }
  assert.match(source, /gunzip/, 'the script decompresses nothing');
});

test('the Windows target is named for Windows and replaces what is already there', async (t) => {
  const server = await h.serveAssets(t);
  const dir = h.makePackage(t, { checksums: h.checksumsFor(ALL_NAMES) });
  // Something already occupying the name, as an upgrade would find.
  fs.writeFileSync(path.join(dir, 'curious.exe'), Buffer.from('an older release'));

  const run = await h.runInstall(t, dir, {
    base: server.origin, platform: 'win32', arch: 'x64',
  });
  assert.strictEqual(run.code, 0, run.output);
  assert.strictEqual(server.requests[0].url, `/v${V}/curious_${V}_windows_amd64.gz`);
  // The gzip header's stored member name never reaches the filesystem,
  // so this name was derived by the script or it is wrong.
  assert.deepStrictEqual(h.installedBinary(dir, 'curious.exe'), h.BINARY_BODY);
  assert.strictEqual(h.installedBinary(dir, 'curious'), null);
});

test('a plaintext origin that is not loopback is refused before any network call', async (t) => {
  const server = await h.serveAssets(t);
  const dir = h.makePackage(t, { checksums: h.checksumsFor(ALL_NAMES) });
  const run = await h.runInstall(t, dir, {
    base: 'http://example.invalid:8099', platform: 'linux', arch: 'x64',
  });
  assert.notStrictEqual(run.code, 0, run.output);
  assert.match(run.output, /https/);
  assert.deepStrictEqual(h.leftovers(dir), []);
  assert.strictEqual(server.requests.length, 0);

  // THE POSITIVE HALF: the same script, over plaintext, to a loopback
  // address, installs. Without it this row is satisfied by refusing
  // every plain http URL, which is a different rule.
  const ok = h.makePackage(t, { checksums: h.checksumsFor(ALL_NAMES) });
  const allowed = await h.runInstall(t, ok, {
    base: server.origin, platform: 'linux', arch: 'x64',
  });
  assert.strictEqual(allowed.code, 0, allowed.output);
  assert.strictEqual(server.requests.length, 1);
});

test('a bracketed loopback address is recognised as one', async (t) => {
  // The spelling the URL parser produces for an address literal keeps
  // its brackets, and the set it is looked up in does not. Without the
  // strip, an origin the rule means to allow is refused for a reason
  // nobody can see.
  const server = await h.serveAssets(t, { host: '::1' });
  const dir = h.makePackage(t, { checksums: h.checksumsFor(ALL_NAMES) });
  const run = await h.runInstall(t, dir, {
    base: server.origin, platform: 'linux', arch: 'x64',
  });
  assert.strictEqual(run.code, 0, run.output);
  assert.strictEqual(server.requests.length, 1);
});

test('below the declared Node floor nothing is downloaded', async (t) => {
  const server = await h.serveAssets(t);
  const dir = h.makePackage(t, { checksums: h.checksumsFor(ALL_NAMES) });
  const declared = JSON.parse(
    fs.readFileSync(path.join(h.PACKAGE_ROOT, 'package.json'), 'utf8')).engines.node;
  const floor = Number(/(\d+)/.exec(declared)[1]);

  const run = await h.runInstall(t, dir, {
    base: server.origin,
    platform: 'linux',
    arch: 'x64',
    nodeVersion: `${floor - 1}.9.9`,
  });
  assert.notStrictEqual(run.code, 0, run.output);
  assert.match(run.output, new RegExp(String(floor)));
  assert.match(run.output, new RegExp(`${floor - 1}\\.9\\.9`));
  // The refusal PRECEDES the network, which is the whole reason it is
  // the first thing the script does.
  assert.strictEqual(server.requests.length, 0);
  assert.deepStrictEqual(h.leftovers(dir), []);

  // THE POSITIVE CONTROL: the very Node running this suite is at or
  // above the floor, and it installs.
  const ok = h.makePackage(t, { checksums: h.checksumsFor(ALL_NAMES) });
  const onFloor = await h.runInstall(t, ok, {
    base: server.origin, platform: 'linux', arch: 'x64', nodeVersion: `${floor}.0.0`,
  });
  assert.strictEqual(onFloor.code, 0, onFloor.output);
  assert.strictEqual(server.requests.length, 1);
});

test('a failed install leaves nothing that claims to be installed', async (t) => {
  const server = await h.serveAssets(t, {
    handler: (record, res, count) => {
      if (count === 1) {
        res.writeHead(200, { 'content-length': String(h.BINARY_GZ.length) });
        res.end(h.BINARY_GZ);
        return;
      }
      res.writeHead(404).end();
    },
  });
  const dir = h.makePackage(t, { checksums: h.checksumsFor(ALL_NAMES) });

  const first = await h.runInstall(t, dir, {
    base: server.origin, platform: 'linux', arch: 'x64',
  });
  assert.strictEqual(first.code, 0, first.output);
  const marker = path.join(dir, 'installed.json');
  assert.ok(fs.existsSync(marker), 'a successful install left no marker');

  // The upgrade: same directory, new version, and the download fails.
  const upgraded = h.makePackage(t, { checksums: h.checksumsFor(ALL_NAMES) });
  fs.copyFileSync(path.join(dir, 'curious'), path.join(upgraded, 'curious'));
  fs.copyFileSync(marker, path.join(upgraded, 'installed.json'));
  const failed = await h.runInstall(t, upgraded, {
    base: server.origin, platform: 'linux', arch: 'x64',
  });
  assert.notStrictEqual(failed.code, 0, failed.output);
  assert.ok(!fs.existsSync(path.join(upgraded, 'installed.json')),
    'a failed install left a marker saying the previous binary is current');
});

test('the gzip the fixture serves really is one', () => {
  // The harness's own claim, checked: if BINARY_GZ were not a gzip, the
  // rows above would be asserting that a script fails to decompress
  // something nothing could decompress.
  assert.deepStrictEqual(zlib.gunzipSync(h.BINARY_GZ), h.BINARY_BODY);
});
