'use strict';

// What Node calls this machine, and what the release calls the file
// built for it. The two vocabularies differ in two places and agree
// everywhere else, which is the whole reason this file exists: a
// mapping written inline at each call site is a mapping that can
// disagree with itself.
//
// ONE HELPER, TWO CONSUMERS, and that is load bearing rather than tidy.
// The install script renames the downloaded binary to binaryName(); the
// shim spawns binaryName(). If those were two expressions, the day one
// of them learned about Windows and the other did not would be the day
// the package installed successfully and then could not run.

// SUPPORTED is the promise this package makes, written out as pairs
// rather than as two lists crossed together. A cross product says
// "every combination", which is a claim about combinations nobody
// built: the release matrix is what decides this, and it is a list of
// pairs there too.
const SUPPORTED = [
  { platform: 'darwin', arch: 'x64' },
  { platform: 'darwin', arch: 'arm64' },
  { platform: 'linux', arch: 'x64' },
  { platform: 'linux', arch: 'arm64' },
  { platform: 'win32', arch: 'x64' },
  { platform: 'win32', arch: 'arm64' },
];

// The two spellings that differ. Node says win32 for an operating
// system that has not been 32-bit in twenty years, and x64 for the
// architecture the build tool calls amd64. Everything else is the same
// word in both vocabularies, so only the differences are written down —
// a full table would invite somebody to "correct" an identity mapping
// and change nothing, or to add a row the release does not build.
const OS_NAMES = { win32: 'windows' };
const ARCH_NAMES = { x64: 'amd64' };

function isSupported(platform, arch) {
  return SUPPORTED.some((p) => p.platform === platform && p.arch === arch);
}

// assetName is the name of the single-member gzip the release publishes
// for this machine, or null when there is none.
//
// NULL RATHER THAN A GUESS. A name composed for an unsupported pair
// would be a request the release host answers with a 404 several
// seconds later, and the reader would be looking at a network error
// rather than at the sentence saying their machine is not built for.
function assetName(version, platform, arch) {
  if (!isSupported(platform, arch)) {
    return null;
  }
  const os = OS_NAMES[platform] || platform;
  const cpu = ARCH_NAMES[arch] || arch;
  return `curious_${version}_${os}_${cpu}.gz`;
}

// binaryName is what the installed file is called once it is in place.
//
// NOTHING HANDS THIS TO US. The archive is a plain gzip, and gunzipping
// bytes ignores the member name the gzip header may or may not carry —
// so the destination is derived here, from the platform, or the install
// succeeds on Windows and leaves a file the shim will never find.
function binaryName(platform) {
  return platform === 'win32' ? 'curious.exe' : 'curious';
}

// unsupportedMessage is the refusal. It names what was found, what is
// supported, and the two ways out, because a person reading it has
// already run the command that was supposed to work.
function unsupportedMessage(platform, arch) {
  const supported = SUPPORTED.map((p) => `  ${p.platform}/${p.arch}`).join('\n');
  return (
    `curious has no prebuilt binary for ${platform}/${arch}.\n\n` +
    `Supported:\n${supported}\n\n` +
    'Two ways forward:\n' +
    '  - download a binary from the releases page at\n' +
    '    https://github.com/curiouspub/cli/releases\n' +
    '  - build it yourself: go install github.com/curiouspub/cli/cmd/curious@latest'
  );
}

module.exports = { SUPPORTED, assetName, binaryName, unsupportedMessage };
