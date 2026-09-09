'use strict';

// What every install row needs: a package directory that is a real copy
// of this one, a server that records what it was actually asked for,
// and a child process running the real script.
//
// NOTHING HERE REIMPLEMENTS THE SCRIPT. The rows assert against files on
// disk and requests a server received, so a helper that computed an
// expected URL, or a digest, from the same expression the script uses
// would be a row agreeing with itself. Where a row needs an expected
// value it is written out or taken from a fixture.

const assert = require('node:assert');
const crypto = require('node:crypto');
const fs = require('node:fs');
const http = require('node:http');
const https = require('node:https');
const net = require('node:net');
const os = require('node:os');
const path = require('node:path');
const zlib = require('node:zlib');
const { spawn } = require('node:child_process');

const { authority } = require('./certs');

const PACKAGE_ROOT = path.join(__dirname, '..', '..');
const PRELOAD = path.join(__dirname, 'fake-platform.js');
// The preload that makes an open file refuse to be removed, which is
// how the one platform nobody here runs behaves. Rows ask for it by
// name so it is visible at the call site rather than switched on by a
// flag somewhere else.
const LOCKED_FILES = path.join(__dirname, 'locked-files.js');

// The version the copied package declares, read from the real
// package.json rather than chosen here: a row asserting the constructed
// URL carries this version is asserting something about the shipped
// file, not about a constant the test invented.
const VERSION = JSON.parse(
  fs.readFileSync(path.join(PACKAGE_ROOT, 'package.json'), 'utf8')).version;

// The fixture "binary": something short, executable on a POSIX machine,
// and recognisable in output. Its gzip and its digest are computed once
// here so a row can compare the installed file against the SAME bytes
// the server sent rather than against a second gunzip of them.
const BINARY_BODY = Buffer.from(
  '#!/bin/sh\necho "curious fixture ok"\n', 'utf8');
const BINARY_GZ = zlib.gzipSync(BINARY_BODY);
const BINARY_DIGEST = crypto.createHash('sha256').update(BINARY_GZ).digest('hex');

// The files a copied package needs to run its own install script. The
// test tree is deliberately absent: the script must work from what the
// tarball carries.
const COPIED = [
  'install.js',
  'package.json',
  'loopback-hosts.json',
  ['lib', 'platform.js'],
  ['bin', 'curious.js'],
];

// shutdown closes a test server and hangs up on whatever is still
// connected to it.
//
// CLOSING ALONE IS NOT ENOUGH, and it cost an afternoon to find out. A
// server's close waits for its existing connections to end, so one
// socket a mutated client never let go of stops the test FILE from
// exiting — not the row, the file — and the run looks like an infinite
// loop rather than like a mutation being caught. The mutation was
// caught; nothing could say so.
function shutdown(server, sockets = []) {
  for (const socket of sockets) {
    socket.destroy();
  }
  return new Promise((resolve) => {
    server.close(resolve);
    // MEASURED, not assumed: an http or https server can hang up on
    // everything it is holding, and a plain socket server cannot —
    // there is no such method there. Calling it anyway throws inside
    // this promise, which the runner reports as a test that never
    // finishes rather than as an error.
    if (typeof server.closeAllConnections === 'function') {
      server.closeAllConnections();
    }
  });
}

function tempDir(t, label) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), `curious-${label}-`));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  return dir;
}

// makePackage copies the real package into a temp directory and writes
// the checksum table the row wants. Everything the script reads comes
// from that copy, so a row can watch what it leaves behind without
// touching the working tree.
function makePackage(t, { checksums = {}, version } = {}) {
  const dir = tempDir(t, 'pkg');
  for (const entry of COPIED) {
    const parts = Array.isArray(entry) ? entry : [entry];
    const from = path.join(PACKAGE_ROOT, ...parts);
    const to = path.join(dir, ...parts);
    fs.mkdirSync(path.dirname(to), { recursive: true });
    fs.copyFileSync(from, to);
  }
  if (version) {
    const manifest = JSON.parse(fs.readFileSync(path.join(dir, 'package.json'), 'utf8'));
    manifest.version = version;
    fs.writeFileSync(path.join(dir, 'package.json'), JSON.stringify(manifest, null, 2));
  }
  fs.writeFileSync(
    path.join(dir, 'checksums.json'), JSON.stringify(checksums, null, 2) + '\n');
  return dir;
}

// checksumsFor is the fixed table a row installs against: the digest of
// the fixture gzip, keyed by the asset name written out in full.
//
// THE DIGEST IS NOT RECOMPUTED FROM WHAT THE SERVER SERVES. A row that
// hashed the payload it was about to send and then asserted the script
// agreed would prove a number equals itself; this table is the fixture,
// and a row that tampers with the payload leaves it alone.
function checksumsFor(names) {
  const table = {};
  for (const name of names) {
    table[name] = BINARY_DIGEST;
  }
  return table;
}

function assetPath(version, osName, arch) {
  return `/v${version}/curious_${version}_${osName}_${arch}.gz`;
}

// serveAssets starts a server that hands back the fixture gzip for any
// path it is given, recording every request with the moment it arrived.
//
// `handler` overrides the default response so a row can redirect, fail,
// stall or corrupt; it is given the recorded request and the response.
function serveAssets(t, { tlsCert = null, handler = null, host = '127.0.0.1' } = {}) {
  const requests = [];
  const connections = [];
  const respond = (req, res) => {
    const record = { method: req.method, url: req.url, at: Date.now(), headers: req.headers };
    record.servername = req.socket.servername || null;
    requests.push(record);
    if (handler) {
      handler(record, res, requests.length);
      return;
    }
    res.writeHead(200, {
      'content-type': 'application/gzip',
      'content-length': String(BINARY_GZ.length),
    });
    res.end(BINARY_GZ);
  };

  const server = tlsCert
    ? https.createServer({ cert: tlsCert.certPem, key: tlsCert.keyPem }, respond)
    : http.createServer(respond);
  server.on('connection', (socket) => connections.push(socket.remotePort));
  server.on('secureConnection', (socket) => connections.push(socket.remotePort));

  t.after(() => shutdown(server));
  return new Promise((resolve) => {
    server.listen(0, host, () => {
      const { port } = server.address();
      resolve({
        server,
        port,
        requests,
        connections,
        origin: `${tlsCert ? 'https' : 'http'}://${host === '::1' ? '[::1]' : host}:${port}`,
      });
    });
  });
}

// runInstall runs the real postinstall in a child process.
//
// THE ENVIRONMENT IS BUILT FROM NOTHING rather than cleaned of the
// variables a row cares about. A proxy row that unsets four names is a
// row that passes until somebody's shell exports a fifth, and the
// failure would look like the code choosing the wrong proxy.
function runInstall(t, dir, {
  base,
  env = {},
  platform = null,
  arch = null,
  nodeVersion = null,
  ca = null,
  preload = [],
} = {}) {
  const passthrough = {};
  for (const key of ['PATH', 'Path', 'HOME', 'USERPROFILE', 'SystemRoot',
    'ComSpec', 'TEMP', 'TMP', 'APPDATA', 'LOCALAPPDATA']) {
    if (process.env[key] !== undefined) {
      passthrough[key] = process.env[key];
    }
  }
  const childEnv = { ...passthrough, ...env };
  if (base) {
    childEnv.CURIOUS_RELEASE_BASE_URL = base;
  }
  if (platform) {
    childEnv.CURIOUS_TEST_PLATFORM = platform;
  }
  if (arch) {
    childEnv.CURIOUS_TEST_ARCH = arch;
  }
  if (nodeVersion) {
    childEnv.CURIOUS_TEST_NODE_VERSION = nodeVersion;
  }
  if (ca) {
    childEnv.NODE_EXTRA_CA_CERTS = ca;
  }

  const args = [];
  if (platform || arch || nodeVersion) {
    args.push('--require', PRELOAD);
  }
  for (const extra of preload) {
    args.push('--require', extra);
  }
  args.push(path.join(dir, 'install.js'));

  const started = Date.now();
  const child = spawn(process.execPath, args, { cwd: dir, env: childEnv });
  let stdout = '';
  let stderr = '';
  child.stdout.on('data', (d) => { stdout += d; });
  child.stderr.on('data', (d) => { stderr += d; });
  return new Promise((resolve, reject) => {
    child.on('error', reject);
    child.on('close', (code, signal) => {
      resolve({ code, signal, stdout, stderr, ms: Date.now() - started, output: stdout + stderr });
    });
  });
}

// installedBinary reads the file the install left behind, or null.
function installedBinary(dir, name = 'curious') {
  const file = path.join(dir, name);
  return fs.existsSync(file) ? fs.readFileSync(file) : null;
}

// leftovers lists everything in the package directory that was not
// copied into it. It is what a "no partial file" row asserts on: naming
// one temp file would only refuse the spelling somebody already thought
// of.
function leftovers(dir) {
  const copied = new Set(['install.js', 'package.json', 'loopback-hosts.json',
    'checksums.json', 'lib', 'bin']);
  return fs.readdirSync(dir).filter((name) => !copied.has(name)).sort();
}

// writeCA puts a certificate authority somewhere a child can be pointed
// at, since that is the only mechanism this package accepts for extra
// trust.
function writeCA(t, pem) {
  const dir = tempDir(t, 'ca');
  const file = path.join(dir, 'ca.pem');
  fs.writeFileSync(file, pem);
  return file;
}

// A CONNECT proxy that records what it was asked to reach and where the
// credentials came from, and tunnels to a port of the harness's
// choosing.
//
// THE REDIRECTED TARGET PORT IS THE MECHANISM behind "the direct
// listener stays untouched". The client is told the asset lives at one
// address; the proxy dials another. A client that ignored the proxy
// would reach the first address, and the first address is watched.
function serveProxy(t, {
  dialPort = null, respondWith = null, respondBody = null, tlsCert = null,
} = {}) {
  const connects = [];
  // EVERY SOCKET THIS PROXY OPENS, tracked. A tunnel is two sockets and
  // the server owns only one of them: closing the server destroys the
  // side it accepted and leaves the side it dialled open, which keeps
  // the whole test file alive with nothing to show for it.
  const open = [];
  const server = tlsCert
    ? https.createServer({ cert: tlsCert.certPem, key: tlsCert.keyPem })
    : http.createServer();

  server.on('request', (req, res) => {
    // Nothing should ever reach here: this proxy speaks CONNECT only.
    res.writeHead(405).end();
  });

  server.on('connect', (req, clientSocket, head) => {
    open.push(clientSocket);
    connects.push({
      target: req.url,
      authorization: req.headers['proxy-authorization'] || null,
      at: Date.now(),
    });
    if (respondWith) {
      // BYTES AFTER THE BLANK LINE BECOME `head` ON THE CLIENT. Node
      // treats every answer to a CONNECT as an upgrade and hands the
      // connect handler whatever followed the headers, so a proxy that
      // refuses with an error page is what actually puts bytes there —
      // and a client that mistook them for the start of a tunnel would
      // report a bewildering handshake failure instead of the refusal.
      const body = respondBody ? Buffer.from(respondBody) : Buffer.alloc(0);
      const head = body.length
        ? `HTTP/1.1 ${respondWith}\r\ncontent-type: text/html\r\n` +
          `content-length: ${body.length}\r\n\r\n`
        : `HTTP/1.1 ${respondWith}\r\n\r\n`;
      clientSocket.write(Buffer.concat([Buffer.from(head, 'ascii'), body]));
      if (respondWith === 'drop') {
        clientSocket.destroy();
      } else {
        clientSocket.end();
      }
      return;
    }
    const upstream = net.connect(dialPort, '127.0.0.1', () => {
      open.push(upstream);
      clientSocket.write('HTTP/1.1 200 Connection established\r\n\r\n');
      if (head && head.length) {
        upstream.write(head);
      }
      upstream.pipe(clientSocket);
      clientSocket.pipe(upstream);
    });
    upstream.on('error', () => clientSocket.destroy());
    clientSocket.on('error', () => upstream.destroy());
    // CLOSE, not only error. A socket destroyed from this side ends
    // without ever erroring, and the other half of the pair would
    // outlive the run.
    clientSocket.on('close', () => upstream.destroy());
    upstream.on('close', () => clientSocket.destroy());
  });

  t.after(() => shutdown(server, open));
  return new Promise((resolve) => {
    server.listen(0, '127.0.0.1', () => {
      const { port } = server.address();
      resolve({
        port,
        connects,
        url: `${tlsCert ? 'https' : 'http'}://127.0.0.1:${port}`,
      });
    });
  });
}

// A listener that answers nothing and counts who arrived. It stands at
// the address a proxy row's URL names, so "no direct request was made"
// is an observation rather than an absence of evidence.
function watchPort(t) {
  const arrivals = [];
  const server = net.createServer((socket) => {
    arrivals.push(Date.now());
    socket.destroy();
  });
  t.after(() => shutdown(server));
  return new Promise((resolve) => {
    server.listen(0, '127.0.0.1', () => {
      resolve({ port: server.address().port, arrivals });
    });
  });
}

// keepSending answers with a body that does not finish on its own, and
// records how it ended.
//
// THE DISTINCTION IT EXISTS FOR is between a client that CLOSED a
// response it had no further use for and one that merely drained it. The
// first hangs up within milliseconds; the second goes on reading, and
// discarding, until this server gives up — and then it is the server
// that ended the body, not the client. `hungUp` is which of those
// happened, and it is a boolean rather than a byte count, so no row has
// to pick a threshold for "too much".
//
// The two numbers belong to the row that calls this: how long it is
// willing to wait for the evidence, and how often to feed the stream.
function keepSending(res, { forMs, everyMs }) {
  const state = { hungUp: null, chunks: 0 };
  const timer = setInterval(() => {
    state.chunks += 1;
    res.write('.'.repeat(64));
  }, everyMs);
  const giveUp = setTimeout(() => {
    clearInterval(timer);
    res.end();
  }, forMs);
  res.on('close', () => {
    state.hungUp = !res.writableEnded;
    clearInterval(timer);
    clearTimeout(giveUp);
  });
  // Writing into a connection the client has dropped is the expected
  // outcome here rather than a fault of the row.
  res.on('error', () => {});
  return state;
}

// assertNoSecretIn is the shape a credential row needs: the value must
// appear nowhere, and something must have happened. An output that is
// empty because nothing ran satisfies the first half on its own.
function assertNoSecretIn(text, secret, evidence) {
  assert.ok(!text.includes(secret), `the output carries the credential:\n${text}`);
  assert.match(text, evidence);
}

module.exports = {
  BINARY_BODY,
  LOCKED_FILES,
  BINARY_GZ,
  BINARY_DIGEST,
  PACKAGE_ROOT,
  VERSION,
  assetPath,
  assertNoSecretIn,
  authority,
  checksumsFor,
  installedBinary,
  keepSending,
  leftovers,
  makePackage,
  runInstall,
  serveAssets,
  serveProxy,
  tempDir,
  watchPort,
  writeCA,
};
