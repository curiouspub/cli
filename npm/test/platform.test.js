'use strict';

// The mapping between what Node calls this machine and what the release
// calls the file built for it.
//
// THESE ROWS SEE A HELPER AND NOTHING ELSE, which is exactly as much as
// they claim. A correct table can sit beside a downloader that asks for
// something different, so every row here has a partner in
// install.test.js that asserts the path a real server actually received.
// Neither half is sufficient alone: the table proves the six names are
// right, the traffic proves the script uses them.

const test = require('node:test');
const assert = require('node:assert');

const platform = require('../lib/platform');

// The exact names the release produces, written out rather than built by
// the same expression the code under test uses. A row that composed the
// expected name from the mapping table would agree with any mapping.
const NAMES = [
  ['darwin', 'x64', 'curious_0.1.0_darwin_amd64.gz'],
  ['darwin', 'arm64', 'curious_0.1.0_darwin_arm64.gz'],
  ['linux', 'x64', 'curious_0.1.0_linux_amd64.gz'],
  ['linux', 'arm64', 'curious_0.1.0_linux_arm64.gz'],
  ['win32', 'x64', 'curious_0.1.0_windows_amd64.gz'],
  ['win32', 'arm64', 'curious_0.1.0_windows_arm64.gz'],
];

test('every supported pair maps to the name the release publishes', () => {
  for (const [osName, arch, want] of NAMES) {
    assert.strictEqual(platform.assetName('0.1.0', osName, arch), want);
  }
});

test('the version is interpolated rather than fixed', () => {
  assert.strictEqual(
    platform.assetName('9.9.9', 'linux', 'x64'),
    'curious_9.9.9_linux_amd64.gz');
});

test('an unsupported pair has no name at all', () => {
  // null rather than a thrown error or a guessed name: the caller's next
  // move is to print the refusal, and a guessed name would 404 several
  // seconds later with nothing saying why.
  assert.strictEqual(platform.assetName('0.1.0', 'linux', 'ppc64'), null);
  assert.strictEqual(platform.assetName('0.1.0', 'aix', 'x64'), null);
});

test('the refusal names what was found and what is supported', () => {
  const message = platform.unsupportedMessage('aix', 'x64');
  assert.match(message, /aix/);
  assert.match(message, /x64/);
  // Every supported pair is listed, spelled the way Node spells it, so a
  // reader can compare it against what the message says was found.
  for (const [osName, arch] of NAMES) {
    assert.ok(
      message.includes(osName + '/' + arch),
      `the supported list omits ${osName}/${arch}: ${message}`);
  }
  // The two ways out, both named.
  assert.match(message, /releases/i);
  assert.match(message, /go install/);
});

test('the binary is named for the platform that will run it', () => {
  assert.strictEqual(platform.binaryName('darwin'), 'curious');
  assert.strictEqual(platform.binaryName('linux'), 'curious');
  assert.strictEqual(platform.binaryName('win32'), 'curious.exe');
});

test('the supported set and the mapping agree in both directions', () => {
  // A pair the mapping can name but the list omits would be installable
  // and undocumented; one the list carries but the mapping cannot name
  // would print a refusal contradicting its own list.
  const listed = platform.SUPPORTED.map((p) => p.platform + '/' + p.arch).sort();
  const mapped = NAMES.map(([osName, arch]) => osName + '/' + arch).sort();
  assert.deepStrictEqual(listed, mapped);
});
