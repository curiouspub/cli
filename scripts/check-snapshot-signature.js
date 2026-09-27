#!/usr/bin/env node
'use strict';

// After a snapshot build, check that every macOS binary was signed with
// the throwaway certificate the build generated.
//
// WHY. The signing step's whole point in a snapshot is to run the path a
// release will run, so that a pin moving under it reds on a pull request
// rather than at a tag. A step that ran and signed nothing would pass
// every other check here, because an unsigned binary archives exactly as
// well as a signed one. So the signature is looked for directly.
//
// WHAT IS LOOKED FOR. The build's certificate is embedded in each
// signature, and its subject is a name nothing else carries. A binary the
// linker signed on its own holds no certificate at all, so the name's
// presence says this certificate signed this file and its absence says
// nothing did.
//
// It reads the release tool's own list of what it built rather than
// walking the directory, so a binary moved to a new path is still found.

const fs = require('node:fs');
const path = require('node:path');

const dist = process.argv[2] || 'dist';
// The leaf's subject, as scripts/snapshot-macos-cert.sh writes it.
const subject = Buffer.from('curious snapshot signing');

function stop(message) {
  console.error(`check-snapshot-signature: ${message}`);
  process.exit(1);
}

let artifacts;
try {
  artifacts = JSON.parse(fs.readFileSync(path.join(dist, 'artifacts.json'), 'utf8'));
} catch (err) {
  stop(`${dist}/artifacts.json could not be read: ${err.message}\n` +
    'This runs after a snapshot build; without a build there is nothing to check.');
}

const binaries = artifacts.filter((a) => a.type === 'Binary' && a.goos === 'darwin');
if (binaries.length === 0) {
  stop('the build lists no macOS binary, so this check observed nothing');
}

const unsigned = binaries
  .map((a) => a.path)
  .filter((p) => !fs.readFileSync(p).includes(subject));

if (unsigned.length > 0) {
  stop(`${unsigned.length} of ${binaries.length} macOS binaries carry no signature by ` +
    `the build's certificate:\n  ${unsigned.join('\n  ')}\n` +
    'The signing step did not run on them. A release would ship these as the\n' +
    'operating system refuses them: quarantined and unsigned.');
}

console.log(`check-snapshot-signature: ${binaries.length} macOS binaries, ` +
  "each signed with the build's certificate");
