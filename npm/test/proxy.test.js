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
//
// proxyTls puts the SAME authority in front of the proxy itself, so one
// trusted-certificate file covers both ends and the row is about the
// scheme the proxy speaks rather than about trust.
async function tunnelled(t, { proxyTls = false, ...proxyOptions } = {}) {
  const ca = h.authority('proxy');
  const caFile = h.writeCA(t, ca.caPem);
  const assets = await h.serveAssets(t, { tlsCert: ca });
  const direct = await h.watchPort(t);
  const proxy = await h.serveProxy(t, {
    dialPort: assets.port,
    ...(proxyTls ? { tlsCert: ca } : {}),
    ...proxyOptions,
  });
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

// bothRoutes is the arrangement every bypass row needs: an asset server
// this machine can reach directly AND a proxy that can reach it too, so
// the row is about which route was chosen rather than about which one
// happens to work.
async function bothRoutes(t, label, host = '127.0.0.1') {
  const ca = h.authority(label);
  const caFile = h.writeCA(t, ca.caPem);
  const assets = await h.serveAssets(t, { tlsCert: ca });
  const proxy = await h.serveProxy(t, { dialPort: assets.port });
  const dir = h.makePackage(t, { checksums: h.checksumsFor([ASSET]) });
  return { ca: caFile, assets, proxy, dir, base: `https://${host}:${assets.port}` };
}

test('a proxy that speaks TLS itself carries the download', async (t) => {
  const s = await tunnelled(t, { proxyTls: true });
  assert.ok(s.proxy.url.startsWith('https://'), s.proxy.url);

  const run = await h.runInstall(t, s.dir, {
    base: s.base, ca: s.ca, ...TARGET,
    env: { HTTPS_PROXY: s.proxy.url },
  });

  assert.strictEqual(run.code, 0, run.output);
  // THE HANDSHAKE IS THE ASSERTION. This proxy answers nothing that is
  // not TLS, so a recorded CONNECT is proof the tunnel was opened over
  // one — a client that dialled it as plain http would be counted here
  // as nothing at all.
  assert.strictEqual(s.proxy.connects.length, 1);
  assert.strictEqual(s.proxy.connects[0].target, `127.0.0.1:${s.direct.port}`);
  assert.strictEqual(s.assets.requests.length, 1);
  assert.deepStrictEqual(h.installedBinary(s.dir, 'curious'), h.BINARY_BODY);
  assert.strictEqual(s.direct.arrivals.length, 0);
});

test('a proxy that refuses with a page of its own is still read as a refusal', async (t) => {
  // THE ONE CONNECT ANSWER THAT CARRIES BYTES. A tunnel to an https
  // host is silent until the client speaks, so a proxy that agrees has
  // nothing to put after its blank line — but a proxy that REFUSES
  // usually has an error page, and the client is handed it as the first
  // bytes of a tunnel that was never opened. Mistaking those for a
  // handshake is how a corporate proxy's login page turns into an
  // inexplicable certificate error.
  const page = '<html><body>Access denied by the gateway policy.</body></html>';
  const s = await tunnelled(t, {
    respondWith: '407 Proxy Authentication Required',
    respondBody: page,
  });

  const run = await h.runInstall(t, s.dir, {
    base: s.base, ca: s.ca, ...TARGET,
    env: { HTTPS_PROXY: s.proxy.url },
  });

  assert.notStrictEqual(run.code, 0, run.output);
  assert.strictEqual(s.proxy.connects.length, 1);
  assert.match(run.output, /407/);
  assert.match(run.output, /HTTPS_PROXY/);
  assert.ok(!/certificate|handshake/i.test(run.output),
    `a proxy refusal was reported as a TLS problem:\n${run.output}`);
  // The page itself is not somebody's problem to read in an install log.
  assert.ok(!run.output.includes('Access denied'), run.output);
  assert.strictEqual(s.direct.arrivals.length, 0);
  assert.strictEqual(h.installedBinary(s.dir, 'curious'), null);
});

test('a port-qualified NO_PROXY entry matches that port and no other', async (t) => {
  const s = await bothRoutes(t, 'no-proxy-port');

  const matching = await h.runInstall(t, s.dir, {
    base: s.base, ca: s.ca, ...TARGET,
    env: { HTTPS_PROXY: s.proxy.url, NO_PROXY: `127.0.0.1:${s.assets.port}` },
  });
  assert.strictEqual(matching.code, 0, matching.output);
  assert.strictEqual(s.proxy.connects.length, 0);
  assert.strictEqual(s.assets.requests.length, 1);
  assert.deepStrictEqual(h.installedBinary(s.dir, 'curious'), h.BINARY_BODY);

  // THE OTHER HALF, or an entry nobody parsed at all would satisfy the
  // first: the same host with a different port is not a match, and the
  // same install goes through the proxy.
  const elsewhere = h.makePackage(t, { checksums: h.checksumsFor([ASSET]) });
  const run = await h.runInstall(t, elsewhere, {
    base: s.base, ca: s.ca, ...TARGET,
    env: { HTTPS_PROXY: s.proxy.url, NO_PROXY: `127.0.0.1:${s.assets.port + 1}` },
  });
  assert.strictEqual(run.code, 0, run.output);
  assert.strictEqual(s.proxy.connects.length, 1);
  assert.strictEqual(s.proxy.connects[0].target, `127.0.0.1:${s.assets.port}`);
  assert.deepStrictEqual(h.installedBinary(elsewhere, 'curious'), h.BINARY_BODY);
});

test('a leading dot in NO_PROXY names the host it is attached to', async (t) => {
  const s = await bothRoutes(t, 'no-proxy-dot');

  const bypassed = await h.runInstall(t, s.dir, {
    base: s.base, ca: s.ca, ...TARGET,
    env: { HTTPS_PROXY: s.proxy.url, NO_PROXY: '.127.0.0.1' },
  });
  assert.strictEqual(bypassed.code, 0, bypassed.output);
  assert.strictEqual(s.proxy.connects.length, 0);
  assert.strictEqual(s.assets.requests.length, 1);
  assert.deepStrictEqual(h.installedBinary(s.dir, 'curious'), h.BINARY_BODY);

  // And a dotted entry for somewhere else is not a match. Without this,
  // an implementation that treated any dotted entry as "bypass
  // everything" would pass the half above.
  const other = h.makePackage(t, { checksums: h.checksumsFor([ASSET]) });
  const run = await h.runInstall(t, other, {
    base: s.base, ca: s.ca, ...TARGET,
    env: { HTTPS_PROXY: s.proxy.url, NO_PROXY: '.example.invalid' },
  });
  assert.strictEqual(run.code, 0, run.output);
  assert.strictEqual(s.proxy.connects.length, 1);
  assert.deepStrictEqual(h.installedBinary(other, 'curious'), h.BINARY_BODY);
});

test('a dotted NO_PROXY entry covers what sits under it', async (t) => {
  // The half of the dot form the loopback rows cannot reach: a host that
  // is not the entry but sits beneath it. Nothing on this machine
  // answers to such a name, so what is observed is that the download was
  // attempted DIRECTLY — the proxy was configured, never dialled, and
  // the failure is about the address rather than about the proxy.
  const s = await bothRoutes(t, 'no-proxy-under');
  const run = await h.runInstall(t, s.dir, {
    base: 'https://storage.example.invalid/x', ca: s.ca, ...TARGET,
    env: { HTTPS_PROXY: s.proxy.url, NO_PROXY: '.example.invalid' },
  });

  assert.notStrictEqual(run.code, 0, run.output);
  assert.strictEqual(s.proxy.connects.length, 0);
  assert.match(run.output, /storage\.example\.invalid/);
  // A proxied attempt would have said so. This one has no proxy in it.
  assert.ok(!/HTTPS_PROXY/.test(run.output), run.output);
});

test('NO_PROXY set to a star turns proxying off altogether', async (t) => {
  const s = await bothRoutes(t, 'no-proxy-star');
  const run = await h.runInstall(t, s.dir, {
    base: s.base, ca: s.ca, ...TARGET,
    env: { HTTPS_PROXY: s.proxy.url, NO_PROXY: '*' },
  });

  assert.strictEqual(run.code, 0, run.output);
  assert.strictEqual(s.proxy.connects.length, 0);
  assert.strictEqual(s.assets.requests.length, 1);
  assert.deepStrictEqual(h.installedBinary(s.dir, 'curious'), h.BINARY_BODY);
});

test('the proxy decision is taken again when a redirect changes host', async (t) => {
  // ONE INSTALL, TWO ANSWERS. The first address is a name the rules do
  // not exempt and the second is an address they do, so a client that
  // decided once and kept the answer gets one of the two hops wrong
  // whichever way it decided.
  const ca = h.authority('reselect');
  const caFile = h.writeCA(t, ca.caPem);
  const assets = await h.serveAssets(t, {
    tlsCert: ca,
    handler: (record, res) => {
      if (record.url.endsWith('.gz')) {
        res.writeHead(302, {
          location: `https://127.0.0.1:${assets.port}/stored/object`,
        });
        res.end();
        return;
      }
      res.writeHead(200, { 'content-length': String(h.BINARY_GZ.length) });
      res.end(h.BINARY_GZ);
    },
  });
  const proxy = await h.serveProxy(t, { dialPort: assets.port });
  const dir = h.makePackage(t, { checksums: h.checksumsFor([ASSET]) });

  const run = await h.runInstall(t, dir, {
    // Port nine is never dialled by this client: the proxy is told the
    // name and this port, and dials the asset server instead.
    base: 'https://localhost:9', ca: caFile, ...TARGET,
    env: { HTTPS_PROXY: proxy.url, NO_PROXY: '127.0.0.1' },
  });

  assert.strictEqual(run.code, 0, run.output);
  assert.deepStrictEqual(h.installedBinary(dir, 'curious'), h.BINARY_BODY);

  // The first hop went through the proxy, and only the first.
  assert.strictEqual(proxy.connects.length, 1);
  assert.strictEqual(proxy.connects[0].target, 'localhost:9');

  // The second arrived directly, which the server can tell apart: a
  // tunnelled request to a name carries that name in the handshake, and
  // a direct one to an address carries no name at all.
  assert.deepStrictEqual(assets.requests.map((r) => r.url),
    [`/v${V}/${ASSET}`, '/stored/object']);
  assert.deepStrictEqual(assets.requests.map((r) => r.servername),
    ['localhost', null]);
});
