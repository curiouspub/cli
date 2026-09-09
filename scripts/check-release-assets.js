#!/usr/bin/env node
'use strict';

// After a release build, check that the archives the npm wrapper will
// ask for are the archives that were actually produced.
//
// WHY THIS EXISTS AT ALL. The release tool's own validator reads the
// configuration as a document: it can say the shape is current and
// cannot say that a compression-only format was handed two files, or
// that a template produced a name spelled differently from the one the
// install script builds. Both of those are build-time facts, and this
// is the only place they are observed.
//
// IT ASKS THE WRAPPER FOR THE NAMES rather than listing them here. Six
// names written out in a second file is two restatements of one fact,
// free to disagree the day one of them is edited — and the disagreement
// would surface as a 404 at somebody's first install.

const fs = require('node:fs');
const path = require('node:path');

const here = path.dirname(__dirname);
const { SUPPORTED, assetName } = require(path.join(here, 'npm', 'lib', 'platform.js'));

const dist = process.argv[2] || 'dist';

function stop(message) {
  console.error(`check-release-assets: ${message}`);
  process.exit(1);
}

let metadata;
try {
  metadata = JSON.parse(fs.readFileSync(path.join(dist, 'metadata.json'), 'utf8'));
} catch (err) {
  stop(`${dist}/metadata.json could not be read: ${err.message}\n` +
    'This runs after a release build; without a build there is nothing to check.');
}
if (!metadata.version) {
  stop(`${dist}/metadata.json names no version`);
}

// The single-member archives, which are the ones the wrapper opens.
// The three-file archive for a person shares the directory and ends
// .tar.gz, so it is excluded by name rather than by hoping.
const produced = fs.readdirSync(dist)
  .filter((name) => name.endsWith('.gz') && !name.endsWith('.tar.gz'))
  .sort();

const wanted = SUPPORTED
  .map(({ platform, arch }) => assetName(metadata.version, platform, arch))
  .sort();

const missing = wanted.filter((name) => !produced.includes(name));
const extra = produced.filter((name) => !wanted.includes(name));

if (missing.length > 0 || extra.length > 0) {
  stop(
    `the release built ${produced.length} single-member archives and the wrapper ` +
    `expects ${wanted.length}.\n` +
    (missing.length > 0 ? `  not built: ${missing.join(', ')}\n` : '') +
    (extra.length > 0 ? `  not asked for: ${extra.join(', ')}\n` : '') +
    'Every name the install script builds comes from the same mapping this\n' +
    'check just used, so a difference here is a difference somebody would\n' +
    'meet as a 404 on their first install.');
}

console.log(
  `check-release-assets: ${produced.length} archives for ${metadata.version}, ` +
  'each one the wrapper knows how to ask for');
