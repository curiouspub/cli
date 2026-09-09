'use strict';

// A preload that makes this machine behave like the one nobody here can
// test on: removing a file while something still holds it open fails,
// rather than quietly succeeding.
//
// WHY IT IS SIMULATED RATHER THAN SKIPPED. On POSIX, unlinking an open
// file is ordinary and the name simply goes away, so a cleanup that
// races the stream's close is invisible here and fatal on Windows —
// where the removal throws EPERM, inside an event handler, which turns a
// download failure that should print one paragraph into an unhandled
// exception, a stack trace, and the partial file still sitting there.
// A property that only misbehaves on one platform is still a property,
// and the alternative to simulating it is discovering it in somebody's
// install log.
//
// It sits beside the preload that makes the script believe it is running
// on another platform, and for the same reason: the test tree ships
// nowhere, so the script under test carries no hook of its own.
//
// WHAT IT MODELS, exactly: a write stream holds its file open from
// creation until its close event, and a removal attempted in that window
// fails the way Windows fails it. Nothing else is touched — a path this
// process never opened is removed normally.

const fs = require('node:fs');

const held = new Set();

const realCreateWriteStream = fs.createWriteStream;
fs.createWriteStream = function createWriteStream(file, ...rest) {
  const stream = realCreateWriteStream.call(this, file, ...rest);
  const key = String(file);
  held.add(key);
  stream.on('close', () => held.delete(key));
  return stream;
};

const realRmSync = fs.rmSync;
fs.rmSync = function rmSync(target, ...rest) {
  const key = String(target);
  if (held.has(key)) {
    const err = new Error(`EPERM: operation not permitted, unlink '${key}'`);
    err.code = 'EPERM';
    err.syscall = 'unlink';
    err.path = key;
    throw err;
  }
  return realRmSync.call(this, target, ...rest);
};
