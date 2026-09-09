#!/usr/bin/env node
'use strict';

// The command `curious` becomes once this package is installed. It runs
// the binary the postinstall downloaded and reports what happened.
//
// ON WINDOWS THIS IS THE SECOND OF THREE LAYERS, which is worth knowing
// before debugging why an argument arrived quoted: npm writes its own
// .cmd and .ps1 wrappers, those invoke node on this file, and this file
// spawns curious.exe. The shebang above matters only on Unix.
//
// IT FORWARDS NO SIGNALS, and that is a decision rather than an
// omission. On a POSIX terminal Ctrl-C goes to the foreground process
// GROUP, which the child has already joined, so forwarding delivers it
// twice. On Windows the signal argument to kill is ignored and the
// process is destroyed outright, so "forwarding an interrupt" there is
// a hard kill wearing a polite name — while a console interrupt already
// reaches the child as a console event. Both platforms already do the
// right thing.
//
// What it owes instead is the exit story below.

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');

const pkg = require('../package.json');
const { binaryName } = require('../lib/platform');

const root = path.join(__dirname, '..');

// The SAME helper the install script renamed the download with. Two
// expressions for one name is how a package installs successfully and
// then cannot find what it installed.
const binary = path.join(root, binaryName(process.platform));

// refuse says what is wrong and what to do about it, and never throws.
// A stack trace here would describe this file to somebody who wants to
// know why their command did not run.
function refuse(what) {
  console.error(
    `\ncurious is not installed properly: ${what}\n\n` +
    'The binary is downloaded by this package\'s postinstall script, so this\n' +
    'usually means the install ran with --ignore-scripts.\n\n' +
    'Install again without it:\n\n' +
    '  npm install --global curiouspub\n\n' +
    'or take a binary from https://github.com/curiouspub/cli/releases\n');
  process.exit(1);
}

// THE MARKER IS CHECKED BEFORE THE BINARY IS RUN, and the version it
// carries is compared rather than merely present. A failed upgrade in a
// directory that already held an older release leaves that release's
// binary sitting under the right name; without this the shim would run
// it and the person would be using a version they did not install.
let marker = null;
try {
  marker = JSON.parse(fs.readFileSync(path.join(root, 'installed.json'), 'utf8'));
} catch {
  marker = null;
}
if (!marker || marker.version !== pkg.version) {
  refuse(marker
    ? `the binary beside it was installed for version ${marker.version}, ` +
      `and this is ${pkg.version}`
    : 'the postinstall step has not run');
}

// stdio: 'inherit' IS the forwarding. Input, output and errors are the
// child's own file descriptors, so nothing here reads or rewrites what
// passes through — which matters for a tool whose output somebody
// redirects to a file.
//
// shell: false keeps argv exactly as it arrived. A command interpreter
// would re-parse it, and a project directory with a space in its name
// would reach the binary as two arguments.
const child = spawn(binary, process.argv.slice(2), { stdio: 'inherit', shell: false });

child.on('error', (err) => {
  if (err.code === 'ENOENT') {
    refuse('the downloaded binary is not there');
  }
  refuse(`the binary could not be started (${err.code || err.message})`);
});

// A process that died of a signal has no exit code. The shell's own
// convention for that is 128 plus the signal number, and reporting a
// bare 1 instead loses the difference between "it failed" and "somebody
// interrupted it".
child.on('exit', (code, signal) => {
  if (code !== null) {
    process.exitCode = code;
    return;
  }
  const number = os.constants.signals[signal];
  process.exitCode = number ? 128 + number : 1;
});
