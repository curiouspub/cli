'use strict';

// Downloading through a proxy, which is how a great many people's
// machines reach anything at all.
//
// EVERY ROW BUILDS ITS OWN ENVIRONMENT FROM NOTHING. The harness passes
// no ambient variables to the child, so a proxy setting on the machine
// running this suite cannot decide any of these outcomes — and a row
// that unset the four names it knew about would still lose to a fifth.
//
// THE PROXY DIALS SOMEWHERE ELSE, deliberately. The client is told the
// asset lives at one address and the proxy connects to another, so
// "the binary arrived through the proxy" and "nothing went direct" are
// two separate observations rather than one hopeful reading of the
// same event.

const test = require('node:test');
const assert = require('node:assert');

const h = require('./helpers/harness');

const V = h.VERSION;
const ASSET = `curious_${V}_linux_amd64.gz`;
const TARGET = { platform: 'linux', arch: 'x64' };

// A trusted https asset server, a watched address the URL will name,
// and a CONNECT proxy that joins the two.
async function tunnelled(t, proxyOptions = {}) {
  const ca = h.authority('proxy');
  const caFile = h.writeCA(t, ca.caPem);
  const assets = await h.serveAssets(t, { tlsCert: ca });
  const direct = await h.watchPort(t);
  const proxy = await h.serveProxy(t, { dialPort: assets.port, ...proxyOptions });
  const dir = h.makePackage(t, { checksums: h.checksumsFor([ASSET]) });
  return {
    ca: caFile,
    assets,
    direct,
    proxy,
    dir,
    base: `https://127.0.0.1:${direct.port}`,
  };
}

test('a configured proxy carries the download, and nothing goes direct', async (t) => {
  const s = await tunnelled(t);
  const run = await h.runInstall(t, s.dir, {
    base: s.base, ca: s.ca, ...TARGET,
    env: { HTTPS_PROXY: s.proxy.url },
  });

  assert.strictEqual(run.code, 0, run.output);
  assert.strictEqual(s.proxy.connects.length, 1);
  // THE PORT COMES FROM THE ADDRESS, not from a hard-coded 443.
  assert.strictEqual(s.proxy.connects[0].target, `127.0.0.1:${s.direct.port}`);
  assert.strictEqual(s.assets.requests.length, 1);
  assert.deepStrictEqual(h.installedBinary(s.dir, 'curious'), h.BINARY_BODY);
  // The address the URL named was never dialled. Without this, a
  // decorative CONNECT beside a direct connection would pass.
  assert.strictEqual(s.direct.arrivals.length, 0);
});

test('npm\'s own proxy setting wins over the ambient one', async (t) => {
  const s = await tunnelled(t);
  // The ambient variable points at a proxy that answers every CONNECT
  // with a refusal, so "npm's setting won" and "neither was used" are
  // different outcomes here.
  const ambient = await h.serveProxy(t, { respondWith: '502 Bad Gateway' });
  const run = await h.runInstall(t, s.dir, {
    base: s.base, ca: s.ca, ...TARGET,
    env: { npm_config_https_proxy: s.proxy.url, HTTPS_PROXY: ambient.url },
  });

  assert.strictEqual(run.code, 0, run.output);
  assert.strictEqual(s.proxy.connects.length, 1);
  assert.strictEqual(ambient.connects.length, 0, 'the ambient proxy was used');
  assert.deepStrictEqual(h.installedBinary(s.dir, 'curious'), h.BINARY_BODY);
});

test('a host in NO_PROXY is reached directly', async (t) => {
  const ca = h.authority('noproxy');
  const caFile = h.writeCA(t, ca.caPem);
  const assets = await h.serveAssets(t, { tlsCert: ca });
  const proxy = await h.serveProxy(t, { dialPort: assets.port });
  const dir = h.makePackage(t, { checksums: h.checksumsFor([ASSET]) });

  const run = await h.runInstall(t, dir, {
    base: `https://127.0.0.1:${assets.port}`, ca: caFile, ...TARGET,
    env: { HTTPS_PROXY: proxy.url, NO_PROXY: '127.0.0.1' },
  });

  assert.strictEqual(run.code, 0, run.output);
  assert.strictEqual(proxy.connects.length, 0);
  // "The proxy saw nothing" on its own is satisfied by an install that
  // never happened. This is the half that says where it went instead.
  assert.strictEqual(assets.requests.length, 1);
  assert.deepStrictEqual(h.installedBinary(dir, 'curious'), h.BINARY_BODY);
});

test('a proxy that refuses fails the install rather than slipping past it', async (t) => {
  const s = await tunnelled(t, { respondWith: '502 Bad Gateway' });
  const run = await h.runInstall(t, s.dir, {
    base: s.base, ca: s.ca, ...TARGET,
    env: { HTTPS_PROXY: s.proxy.url },
  });

  assert.notStrictEqual(run.code, 0, run.output);
  // The proxy really was dialled: an implementation that gave up before
  // ever reaching it cannot satisfy this row.
  assert.strictEqual(s.proxy.connects.length, 1);
  assert.match(run.output, /HTTPS_PROXY/);
  assert.ok(run.output.includes(s.proxy.url), `the message does not name the proxy:\n${run.output}`);
  // NEVER QUIETLY DIRECT. Falling back turns a blocked egress policy
  // into an unnoticed bypass.
  assert.strictEqual(s.direct.arrivals.length, 0);
  assert.strictEqual(s.assets.requests.length, 0);
  assert.strictEqual(h.installedBinary(s.dir, 'curious'), null);
});

test('a proxy asking for credentials says so, rather than failing as TLS', async (t) => {
  const s = await tunnelled(t, { respondWith: '407 Proxy Authentication Required' });
  const run = await h.runInstall(t, s.dir, {
    base: s.base, ca: s.ca, ...TARGET,
    env: { HTTPS_PROXY: s.proxy.url },
  });

  assert.notStrictEqual(run.code, 0, run.output);
  assert.match(run.output, /407/);
  assert.match(run.output, /proxy/i);
  // Without the status check a refusal surfaces as a bewildering error
  // about a handshake that never happened.
  assert.ok(!/certificate|handshake/i.test(run.output),
    `a proxy refusal was reported as a TLS problem:\n${run.output}`);
});

test('credentials in the proxy address are sent to the proxy and printed nowhere', async (t) => {
  const ca = h.authority('creds');
  const caFile = h.writeCA(t, ca.caPem);
  // The server listens on the loopback address; the URL calls it by
  // name. Only the proxy ever dials, so the name in the address is
  // there to be checked against the certificate rather than resolved.
  const assets = await h.serveAssets(t, { tlsCert: ca });
  const proxy = await h.serveProxy(t, { dialPort: assets.port });
  const dir = h.makePackage(t, { checksums: h.checksumsFor([ASSET]) });
  const password = 'sw0rdf1sh-not-in-any-output';

  const run = await h.runInstall(t, dir, {
    base: `https://localhost:9/`, ca: caFile, ...TARGET,
    env: { HTTPS_PROXY: `http://someone:${password}@127.0.0.1:${proxy.port}` },
  });

  assert.strictEqual(run.code, 0, run.output);
  assert.strictEqual(proxy.connects.length, 1);
  const sent = proxy.connects[0].authorization;
  assert.ok(sent, 'no proxy credentials were sent');
  assert.strictEqual(
    Buffer.from(sent.replace(/^Basic /i, ''), 'base64').toString('utf8'),
    `someone:${password}`);
  // TIED TO A REAL DOWNLOAD, or dead code satisfies the row.
  assert.deepStrictEqual(h.installedBinary(dir, 'curious'), h.BINARY_BODY);
  // The name goes in the CONNECT target with its port; the certificate
  // is checked against the name without one.
  assert.strictEqual(proxy.connects[0].target, 'localhost:9');
  assert.strictEqual(assets.requests[0].servername, 'localhost');
  // A success prints nothing at all, which is the strongest form this
  // assertion can take.
  assert.strictEqual(run.output, '');
});

test('a failing credentialled proxy is named without its password', async (t) => {
  const password = 'hunter2-must-not-appear';
  const s = await tunnelled(t, { respondWith: '502 Bad Gateway' });
  const proxyURL = `http://someone:${password}@127.0.0.1:${s.proxy.port}`;
  const run = await h.runInstall(t, s.dir, {
    base: s.base, ca: s.ca, ...TARGET,
    env: { HTTPS_PROXY: proxyURL },
  });

  assert.notStrictEqual(run.code, 0, run.output);
  // A failure message is where a credential actually leaks: the success
  // path prints nothing, so this is the only path that could.
  h.assertNoSecretIn(run.output, password, /HTTPS_PROXY/);
  assert.match(run.output, /someone/);
  assert.match(run.output, /\*\*\*/);
});

test('an address literal is bracketed for the proxy and bare for the certificate', async (t) => {
  const ca = h.authority('literal');
  const caFile = h.writeCA(t, ca.caPem);
  const assets = await h.serveAssets(t, { tlsCert: ca });
  const proxy = await h.serveProxy(t, { dialPort: assets.port });
  const dir = h.makePackage(t, { checksums: h.checksumsFor([ASSET]) });

  const run = await h.runInstall(t, dir, {
    base: 'https://[::1]:9/', ca: caFile, ...TARGET,
    env: { HTTPS_PROXY: proxy.url },
  });

  // The two spellings differ, and mixing them fails confusingly: a
  // bracketed name in the certificate check matches no entry in the
  // certificate, and an unbracketed one in the CONNECT line is not a
  // host and a port.
  assert.strictEqual(run.code, 0, run.output);
  assert.strictEqual(proxy.connects[0].target, '[::1]:9');
  assert.deepStrictEqual(h.installedBinary(dir, 'curious'), h.BINARY_BODY);
});

test('a proxy address this script does not understand is refused', async (t) => {
  const s = await tunnelled(t);
  const run = await h.runInstall(t, s.dir, {
    base: s.base, ca: s.ca, ...TARGET,
    env: { HTTPS_PROXY: `socks5://127.0.0.1:${s.proxy.port}` },
  });

  assert.notStrictEqual(run.code, 0, run.output);
  assert.match(run.output, /socks5/);
  assert.match(run.output, /HTTPS_PROXY/);
  // Not treated as plain http, and not quietly ignored either.
  assert.strictEqual(s.proxy.connects.length, 0);
  assert.strictEqual(s.direct.arrivals.length, 0);
});
