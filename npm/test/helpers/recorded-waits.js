'use strict';

// A preload that writes down every wait the install script asks the
// timer for, and when each one fires, while still letting it run.
//
// A STOPWATCH ON A SHARED MACHINE MEASURES THE MACHINE. A hosted runner
// can stall every process on it for seconds, which stretches the pause
// a server sees between two attempts without the script having chosen
// anything different. What the script chose is what it asked the timer
// for, and that is what is written down here.
//
// NOTHING IS FAKED. Every call goes to the real timer with the same
// delay and arguments, and the real handle comes back, so each pause is
// really spent and clearing a deadline still clears it. A recorder that
// ended the waits at once would be a fake clock, and a row reading it
// would pass a script that never waits at all.
//
// THE FIRING IS WRITTEN DOWN AS WELL AS THE ASKING. A script that asks
// for a pause and does not wait for it arms the next attempt's deadline
// before the pause fires, and the order of the lines shows that however
// slow the machine was.
//
// Each line is written synchronously, because the script exits the
// moment its last attempt fails and a write still pending then is lost.
//
// It sits beside the other preloads, and for the same reason: the test
// tree ships nowhere, so the script under test carries no hook of its
// own. The file it writes to is named by a variable only this preload
// reads.

const fs = require('node:fs');

const record = process.env.CURIOUS_TEST_WAITS_FILE;
if (!record) {
  throw new Error('recorded-waits.js was loaded without CURIOUS_TEST_WAITS_FILE');
}

const realSetTimeout = globalThis.setTimeout;
globalThis.setTimeout = function setTimeout(callback, ms, ...rest) {
  fs.appendFileSync(record, `set ${ms}\n`);
  return realSetTimeout(function fired(...args) {
    fs.appendFileSync(record, `fired ${ms}\n`);
    return callback.apply(this, args);
  }, ms, ...rest);
};
