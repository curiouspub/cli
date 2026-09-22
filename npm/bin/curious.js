#!/usr/bin/env node
'use strict';

// The command `curious` becomes once this package is installed. It runs
// the binary for this platform, FETCHING IT FIRST IF IT IS NOT THERE,
// and reports what happened.
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
//
// IT NO LONGER MENTIONS --ignore-scripts, and that is the point of this
// file's change rather than an omission: a missing binary is now FETCHED
// rather than reported, so by the time anything here refuses, the fetch
// has been tried and failed for a reason of its own.
function refuse(what) {
  console.error(
    `\ncurious is not installed properly: ${what}\n\n` +
    'Take a binary from https://github.com/curiouspub/cli/releases, or\n' +
    'install again:\n\n' +
    '  npm install --global curiouspub\n');
  process.exit(1);
}

// THE MARKER IS CHECKED BEFORE THE BINARY IS RUN, and the version it
// carries is compared rather than merely present. A failed upgrade in a
// directory that already held an older release leaves that release's
// binary sitting under the right name; without this the shim would run
// it and the person would be using a version they did not install.
function markerIsCurrent() {
  try {
    const marker = JSON.parse(fs.readFileSync(path.join(root, 'installed.json'), 'utf8'));
    return Boolean(marker) && marker.version === pkg.version;
  } catch {
    return false;
  }
}

// fetchThenRun is what a package manager that refuses install scripts
// leaves this command to do for itself.
//
// # Why the fetch lives here as well as in the postinstall
//
// The postinstall is the eager path and stays: where install scripts run,
// the binary is in place before anybody types the command and this
// function is never reached. What it cannot do is run at all when the
// package manager declines to run it — and npm has said in its own
// documentation that a future release will BLOCK unreviewed install
// scripts rather than warn.
//
// The approval that would answer that is a field in the INSTALLING
// PROJECT'S package.json, which two of the three ways this tool is
// installed do not have: a global install has no project, and npx has no
// install step to approve at. So a documented flag could never have
// covered them, and the fetch moves to where every path passes through.
//
// # And it repairs a state that used to stick
//
// npx caches the package it fetched. Once that cache held a package whose
// binary was never downloaded, every later `npx curiouspub` reused it and
// failed again — after the cause was gone, with nothing in the message
// about a cache. Fetching here makes that self-repairing: the binary is
// missing, so it is fetched, and the run proceeds.
async function fetchThenRun() {
  // ON STDERR, because `curious version` is a command whose output
  // somebody pipes. A progress line on stdout would land in their file.
  process.stderr.write(
    'curious: fetching the binary for your platform (first run)...\n');
  const installer = require('../install.js');
  try {
    await installer.main();
  } catch (err) {
    console.error(
      `\ncurious could not fetch its binary.\n\n${installer.explain(err)}\n`);
    process.exit(1);
  }
  run();
}

// stdio: 'inherit' IS the forwarding. Input, output and errors are the
// child's own file descriptors, so nothing here reads or rewrites what
// passes through — which matters for a tool whose output somebody
// redirects to a file.
//
// shell: false keeps argv exactly as it arrived. A command interpreter
// would re-parse it, and a project directory with a space in its name
// would reach the binary as two arguments.
function run() {
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
}

if (markerIsCurrent()) {
  run();
} else {
  fetchThenRun();
}
