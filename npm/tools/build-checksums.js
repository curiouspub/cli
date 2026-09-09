#!/usr/bin/env node
'use strict';

// Turn the release's own checksum file into the table this package
// ships, and refuse to publish if the two have got out of step.
//
// THIS IS THE ONLY THING ENFORCING THE INVARIANT THE DOWNLOAD ADDRESS
// RESTS ON. That address puts the version in twice, in two spellings —
// the tag keeps its leading v and the asset name does not — and it is
// built from this package's own version. So the tag being published,
// the version in package.json and the version inside every key of this
// table have to be the same thing, and nothing else in the pipeline
// compares them.
//
// IT SHIPS TO NOBODY. The package's allowlist of published files does
// not include this directory: it runs once, in the release, on a
// machine that already has the artefacts.

const fs = require('node:fs');
const path = require('node:path');

const pkg = require('../package.json');
const { SUPPORTED, assetName } = require('../lib/platform');

function stop(message) {
  console.error(`build-checksums: ${message}`);
  process.exit(1);
}

function argument(name) {
  const at = process.argv.indexOf(`--${name}`);
  if (at < 0 || at + 1 >= process.argv.length) {
    stop(`--${name} is required`);
  }
  return process.argv[at + 1];
}

const tag = argument('tag');
const source = argument('checksums');
const destination = argument('out');

// The tag, minus its leading v, IS the package version. A registry
// forbids the v and the release address requires it, so they are two
// spellings of one number rather than two numbers.
const version = tag.replace(/^v/, '');
if (version !== pkg.version) {
  stop(
    `the tag ${tag} does not match the package version ${pkg.version}.\n` +
    'Every download address this package builds comes from the package\n' +
    'version, so publishing this pair would ship a package that asks the\n' +
    'release for a version that is not there.');
}

let text;
try {
  text = fs.readFileSync(source, 'utf8');
} catch (err) {
  stop(`${source} could not be read: ${err.message}`);
}

// The release tool writes one line per artefact: a digest, whitespace,
// a name. Everything that is not a line of that shape is skipped rather
// than guessed at.
const digests = new Map();
for (const line of text.split('\n')) {
  const fields = line.trim().split(/\s+/);
  if (fields.length < 2) {
    continue;
  }
  digests.set(fields[fields.length - 1], fields[0]);
}
if (digests.size === 0) {
  stop(`${source} lists no artefacts at all`);
}

// EVERY SUPPORTED PLATFORM, NAMED BY THE SAME MAPPING THE INSTALL
// SCRIPT USES. A table with five of six entries publishes successfully
// and then refuses to install on the sixth platform, in the field,
// after publication.
const table = {};
const missing = [];
for (const { platform, arch } of SUPPORTED) {
  const name = assetName(version, platform, arch);
  const digest = digests.get(name);
  if (!digest) {
    missing.push(name);
    continue;
  }
  if (!/^[0-9a-f]{64}$/.test(digest)) {
    stop(`${name} carries "${digest}", which is not a SHA-256 digest`);
  }
  table[name] = digest;
}
if (missing.length > 0) {
  stop(
    `${source} has no entry for:\n  ${missing.join('\n  ')}\n` +
    'The release did not build every platform this package promises, or it\n' +
    'named them differently. Either way this package must not be published.');
}

fs.mkdirSync(path.dirname(destination), { recursive: true });
fs.writeFileSync(destination, JSON.stringify(table, null, 2) + '\n');

// READ BACK WHAT WAS WRITTEN. The checks above are about the input;
// this one is about the file that will actually ship, which is the
// thing the claim is being made about.
const written = JSON.parse(fs.readFileSync(destination, 'utf8'));
const keys = Object.keys(written);
if (keys.length !== SUPPORTED.length) {
  stop(`${destination} ended up with ${keys.length} entries, want ${SUPPORTED.length}`);
}
for (const key of keys) {
  if (!key.includes(`_${pkg.version}_`)) {
    stop(`${destination} carries ${key}, which is not version ${pkg.version}`);
  }
}
console.log(`build-checksums: ${keys.length} digests for ${pkg.version} -> ${destination}`);
