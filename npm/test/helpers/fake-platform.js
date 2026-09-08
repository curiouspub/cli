'use strict';

// A preload that makes the install script BELIEVE it is running
// somewhere else, so the rows about other platforms observe real files
// and real requests instead of interrogating a helper.
//
// IT LIVES IN THE TEST TREE AND SHIPS NOWHERE. The package's own
// allowlist of published files does not include test/, so nothing here
// reaches a user's machine — which is the whole reason the install
// script carries no test hook of its own. A script that can be told
// which platform it is on by an environment variable has a switch a
// stranger can also flip.
//
// WHAT IT DOES NOT CHANGE, and it matters: the path module resolved its
// own separator during Node's own bootstrap, long before a preload
// runs. So a script that believes it is on Windows still joins paths
// the way the real machine does, which is what lets a POSIX machine
// observe the Windows rename target without pretending to have a
// Windows filesystem.

function pretend(object, key, value) {
  if (value) {
    Object.defineProperty(object, key, { value, configurable: true });
  }
}

pretend(process, 'platform', process.env.CURIOUS_TEST_PLATFORM);
pretend(process, 'arch', process.env.CURIOUS_TEST_ARCH);
pretend(process.versions, 'node', process.env.CURIOUS_TEST_NODE_VERSION);
