#!/usr/bin/env node
'use strict';

// Fetch the curious binary this machine can run, check it against a
// digest that shipped inside this package, and put it in place.
//
// WHAT VOUCHES FOR WHAT, because a script that downloads and executes a
// binary is mechanically what a malicious package does, and the whole
// difference is here:
//
//   - Transport is TLS to the release host. That says the bytes arrived
//     unaltered from whoever answered.
//   - The SHA-256 comes from checksums.json, which is inside the npm
//     package you already installed. It is written when the package is
//     published, from the same run that built the binaries. So a
//     release asset REPLACED AFTER PUBLICATION fails this check,
//     because the digest is vouched for by the registry rather than by
//     the host serving the download.
//   - That is the whole claim, and it is narrower than it may sound. It
//     assumes this npm package is itself authentic. It says nothing
//     about a compromised publisher or a compromised release run: a run
//     that builds a binary and its checksum together will happily vouch
//     for its own output. That residual is not closable by anything a
//     dependency-free install script can do.
//   - The release is also signed, keylessly, over its checksum file.
//     That signature is for an auditor with the verifying tool. This
//     script does not check it and does not pretend to.
//
// NO DEPENDENCIES, and that is part of the argument rather than a
// preference. Everything here is Node's standard library, so a reader
// can audit the whole thing in one sitting — which is the only real
// answer to "why should I let this run on my machine". It is also why
// the proxy support further down is thirty lines of tunnel rather than
// a package: there is no built-in proxy support to switch on, so the
// choice was a dependency tree inside a postinstall or this.

const crypto = require('node:crypto');
const fs = require('node:fs');
const http = require('node:http');
const https = require('node:https');
const net = require('node:net');
const path = require('node:path');
const tls = require('node:tls');
const zlib = require('node:zlib');

const pkg = require('./package.json');
const platform = require('./lib/platform');
const loopback = require('./loopback-hosts.json');

// Where the release lives. CURIOUS_RELEASE_BASE_URL replaces it, which
// is what makes this script testable against a server on this machine.
//
// EXPOSING THAT OVERRIDE IS SAFE BECAUSE OF WHAT SITS BEHIND IT: the
// digest is embedded in this package, so pointing the download
// somewhere else can only ever produce a MISMATCH. There is no origin
// an attacker can name that makes a substituted binary verify.
const DEFAULT_RELEASE_BASE = 'https://github.com/curiouspub/cli/releases/download';

// Thirty seconds for one whole attempt, redirects included.
//
// IT IS A DEADLINE PER ATTEMPT AND NOT PER HOP: a chain of redirects
// would otherwise multiply a bound nobody chose. It is not a total
// across attempts either — a retry gets a fresh one, because the thing
// being bounded is "this fetch is not going to work", and the second
// fetch is a different fetch.
//
// The number is chosen for what it bounds HERE, and deliberately not
// carried from anywhere else in this project. The asset is a few
// megabytes; thirty seconds covers it down to roughly a megabit per
// second, which is the slowest link on which installing a development
// tool is a reasonable thing to be doing.
const ATTEMPT_TIMEOUT_MS = 30_000;

// Three attempts, and therefore TWO waits — three attempts have two
// gaps between them, and a third backoff term would never be reached.
//
// A second, and then two. It is sized for a delivery-network blip or a
// failover, which is what a storage host that has just failed is likely
// to be doing. The quarter-second pause this project uses before
// re-asking an endpoint that answers in milliseconds is the number NOT
// to reuse here: right for smoothing a stalled small request, absurd as
// a pause before re-asking a file server.
//
// Worst case is therefore ninety-three seconds. That is derived, not a
// third constant, and nothing here compares against it.
const ATTEMPTS = 3;
const BACKOFF_MS = [1_000, 2_000];

// How many redirects one attempt will follow. The release host hands
// downloads to a storage host, which is one hop; five leaves room for a
// chain nobody planned while still making a loop terminate, which is
// the only thing a bound is really for.
const MAX_REDIRECTS = 5;

// ---------------------------------------------------------------------
// Stopping.
// ---------------------------------------------------------------------

// Refused carries a message already written for a person. Everything
// that decides to stop throws one, and exactly one place prints it, so
// there is a single answer to "what does a failed install look like".
class Refused extends Error {}

// Retryable is a failure worth asking about again. The difference from
// Refused decides whether the two waits happen at all: a 404 will say
// the same thing in a second's time, and a reset connection may not.
class Retryable extends Error {}

// ---------------------------------------------------------------------
// Step 0: the Node floor, before anything else and before any network.
// ---------------------------------------------------------------------

// THE DECLARED FLOOR IS THE ONLY FLOOR. It is read out of this
// package's own engines field rather than written again here, so there
// is one number and a change to it moves the check with it.
//
// engines ALONE IS NOT A FLOOR: npm only warns about it unless the
// person installing has asked for strictness, so the check has to
// happen here. And it has to happen FIRST — a version check that runs
// after the download has let the thing it was guarding already happen.
function requireNodeFloor() {
  const declared = (pkg.engines && pkg.engines.node) || '';
  const stated = /^>=\s*(\d+)/.exec(declared);
  if (!stated) {
    throw new Refused(
      `This package declares a Node requirement of "${declared}", which this\n` +
      'installer cannot read. That means the package was built wrong rather\n' +
      'than anything about your machine. Please report it.');
  }
  const floor = Number(stated[1]);
  const found = process.versions.node;
  if (Number(found.split('.')[0]) < floor) {
    throw new Refused(
      `curious needs Node ${declared} and this is Node ${found}.\n\n` +
      `Upgrade Node to ${floor} or newer and install again. Nothing has been\n` +
      'downloaded.');
  }
}

// ---------------------------------------------------------------------
// Step 1: what to fetch, and what its digest must be.
// ---------------------------------------------------------------------

function resolveTarget() {
  const asset = platform.assetName(pkg.version, process.platform, process.arch);
  if (!asset) {
    throw new Refused(platform.unsupportedMessage(process.platform, process.arch));
  }
  return { asset, binary: platform.binaryName(process.platform) };
}

// expectedDigest looks the asset up BEFORE any network call.
//
// AN ABSENT DIGEST IS NOT A MISMATCH, and it is not the same branch.
// A table with no entry for this platform means the package was built
// wrong — a stale file, a generator that emitted different names — and
// the shape everyone writes by accident, "if there is a digest and it
// differs, refuse", installs whatever arrived. So a missing entry stops
// here, naming the key it wanted, before anything is fetched.
function expectedDigest(asset) {
  const file = path.join(__dirname, 'checksums.json');
  let table;
  try {
    table = JSON.parse(fs.readFileSync(file, 'utf8'));
  } catch (err) {
    throw new Refused(
      `The checksum table that ships with this package could not be read:\n\n  ${err.message}\n\n` +
      'That is a fault in the package rather than anything about your\n' +
      'machine. Please report it.');
  }
  const digest = table[asset];
  if (typeof digest !== 'string' || !/^[0-9a-f]{64}$/.test(digest)) {
    throw new Refused(
      `This package carries no checksum for ${asset}.\n\n` +
      'Every supported platform is meant to have one, so this package was\n' +
      'built wrong and curious will not install a binary it cannot check.\n' +
      'Please report it.');
  }
  return digest;
}

// ---------------------------------------------------------------------
// The scheme rule. ONE rule, applied to the first address and to every
// hop after it.
//
// It is one rule rather than two on purpose. "https everywhere, except
// the first address may be loopback http" makes a loopback server that
// issues a redirect abort on its own redirect — which is the ordinary
// way anybody would exercise redirects on their own machine.
// ---------------------------------------------------------------------

// A host is loopback if it is in the set this project keeps in one
// place, in loopback-hosts.json, which the Go client's own copy is held
// equal to by a test.
//
// THE BRACKETS COME OFF FIRST. A URL parser reports an address literal
// with its brackets on and the set holds the address without them, so
// without this an origin the rule means to allow is refused for a
// reason nobody could see.
function unbracket(hostname) {
  return hostname.startsWith('[') && hostname.endsWith(']')
    ? hostname.slice(1, -1)
    : hostname;
}

function isLoopback(hostname) {
  return loopback.hosts.includes(unbracket(hostname).toLowerCase());
}

function checkScheme(url, where) {
  if (url.protocol === 'https:') {
    return;
  }
  if (url.protocol === 'http:' && isLoopback(url.hostname)) {
    return;
  }
  throw new Refused(
    `${where} is ${url.protocol}//${url.host}, and curious will only download\n` +
    'over https — or over plain http to this machine, which is there for\n' +
    'local development.\n\n' +
    'Nothing has been downloaded.');
}

function releaseBase() {
  const override = process.env.CURIOUS_RELEASE_BASE_URL;
  if (!override) {
    return DEFAULT_RELEASE_BASE;
  }
  let url;
  try {
    url = new URL(override);
  } catch {
    throw new Refused(
      'CURIOUS_RELEASE_BASE_URL is not a URL. Unset it to use the real release.');
  }
  checkScheme(url, 'The release origin CURIOUS_RELEASE_BASE_URL names');
  return override.replace(/\/+$/, '');
}

// The tag keeps its leading v and the asset name does not. They are the
// same version in two spellings inside one address, and getting it
// wrong is a 404 nobody can explain.
function downloadURL(asset) {
  return `${releaseBase()}/v${pkg.version}/${asset}`;
}

// ---------------------------------------------------------------------
// Step 3: the download.
// ---------------------------------------------------------------------

// ---------------------------------------------------------------------
// Proxies.
//
// Node's https client ignores every proxy variable there is: there is
// no built-in support to switch on, so the choice was a dependency
// inside a postinstall or the thirty lines below. Given that this
// script's whole security argument is that a reader can audit it in one
// sitting, thirty auditable lines beat a dependency tree.
// ---------------------------------------------------------------------

// NPM'S OWN SETTINGS COME FIRST. Somebody who configured npm expects
// that to be honoured, and npm resolves its own configuration ahead of
// the ambient environment.
const PROXY_VARIABLES = [
  'npm_config_https_proxy', 'npm_config_proxy', 'HTTPS_PROXY', 'https_proxy',
];
const NO_PROXY_VARIABLES = ['npm_config_no_proxy', 'NO_PROXY', 'no_proxy'];

function firstSet(names) {
  for (const name of names) {
    const value = process.env[name];
    if (typeof value === 'string' && value.trim() !== '') {
      return { name, value: value.trim() };
    }
  }
  return null;
}

function defaultPort(url) {
  return url.port || (url.protocol === 'https:' ? '443' : '80');
}

// splitEntry reads one NO_PROXY entry into a host and an optional port,
// keeping an address literal's colons out of the port's way.
function splitEntry(entry) {
  if (entry.startsWith('[')) {
    const close = entry.indexOf(']');
    return {
      host: entry.slice(1, close),
      port: entry.slice(close + 1).replace(/^:/, ''),
    };
  }
  const colon = entry.indexOf(':');
  if (colon >= 0 && entry.indexOf(':', colon + 1) < 0) {
    return { host: entry.slice(0, colon), port: entry.slice(colon + 1) };
  }
  return { host: entry, port: '' };
}

// Host and domain matching, with the three forms people actually write:
// a bare host, a leading dot for "and everything under it", and the
// wildcard that turns proxying off altogether.
function bypassed(url) {
  const rule = firstSet(NO_PROXY_VARIABLES);
  if (!rule) {
    return false;
  }
  const host = unbracket(url.hostname).toLowerCase();
  const port = defaultPort(url);
  for (const raw of rule.value.split(',')) {
    const entry = raw.trim().toLowerCase();
    if (!entry) {
      continue;
    }
    if (entry === '*') {
      return true;
    }
    const parsed = splitEntry(entry);
    if (parsed.port && parsed.port !== port) {
      continue;
    }
    // THE DOT IS THE WHOLE DIFFERENCE BETWEEN TWO OF THE THREE FORMS,
    // and taking it off before the comparison collapses them: every
    // entry becomes a domain-wide one, so a bare example.com bypasses
    // evil.example.com as well.
    //
    // THAT ERROR RUNS IN THE DANGEROUS DIRECTION. It turns the proxy
    // OFF for hosts nobody exempted, so on a machine whose egress is
    // controlled the download goes direct where the policy said tunnel
    // — the same thing this script refuses to do when a tunnel fails,
    // arriving from the other side: not falling back to direct, but
    // never choosing the proxy at all.
    const under = parsed.host.startsWith('.');
    const wanted = under ? parsed.host.slice(1) : parsed.host;
    if (host === wanted || (under && host.endsWith(`.${wanted}`))) {
      return true;
    }
  }
  return false;
}

// maskProxy renders a proxy address for a message. The password is the
// half that must never be printed; the username stays, because a person
// reading a failure needs to recognise which setting is in play.
function maskProxy(url) {
  const shown = new URL(url.toString());
  if (shown.password) {
    shown.password = '***';
  }
  return shown.toString();
}

function proxyFailure(proxy, why) {
  return (
    `curious is set up to reach the release through a proxy, and ${why}.\n\n` +
    `  ${proxy.name}=${maskProxy(proxy.url)}\n\n` +
    'curious will not connect directly instead. On a machine where direct\n' +
    'access is blocked that would be a bypass nobody asked for, and where it\n' +
    'is not, it would hide the real problem behind a slower one.\n\n' +
    'Nothing was downloaded.');
}

// proxyFor decides, for THIS address, whether a proxy applies. It is
// asked again on every redirect: a redirect that crosses hosts leaves
// the old tunnel pointing at the wrong origin.
//
// A PERMITTED PLAIN-HTTP ADDRESS IS A PLAIN REQUEST. Tunnelling is for
// https, and the only http this script will touch at all is loopback.
function proxyFor(url) {
  if (url.protocol !== 'https:') {
    return null;
  }
  const configured = firstSet(PROXY_VARIABLES);
  if (!configured) {
    return null;
  }
  let parsed;
  try {
    parsed = new URL(configured.value);
  } catch {
    throw new Refused(
      `The proxy address in ${configured.name} is not a URL.\n\n` +
      'Fix it or unset it; curious will not ignore a proxy that was\n' +
      'configured on purpose.');
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    // REFUSED RATHER THAN TREATED AS HTTP. Guessing at a scheme this
    // script does not speak would dial the proxy with the wrong
    // protocol and report whatever came back as a network fault.
    throw new Refused(
      `The proxy address in ${configured.name} uses the ` +
      `${parsed.protocol.replace(':', '')} scheme, which curious does not speak.\n\n` +
      `  ${configured.name}=${maskProxy(parsed)}\n\n` +
      'curious tunnels through http and https proxies only. Nothing was\n' +
      'downloaded.');
  }
  if (bypassed(url)) {
    return null;
  }
  return { name: configured.name, url: parsed };
}

// openTunnel asks the proxy to join us to the target and hands back the
// raw socket. THE PORT COMES FROM THE ADDRESS rather than a hard-coded
// 443, and the address literal keeps its brackets here — a CONNECT line
// is a host and a port, and an unbracketed literal is neither.
function openTunnel(proxy, target, signal, sockets) {
  return new Promise((resolve, reject) => {
    const module = proxy.url.protocol === 'https:' ? https : http;
    const authority = `${target.hostname}:${defaultPort(target)}`;
    const headers = { host: authority };
    if (proxy.url.username) {
      const user = decodeURIComponent(proxy.url.username);
      const secret = decodeURIComponent(proxy.url.password);
      headers['proxy-authorization'] =
        `Basic ${Buffer.from(`${user}:${secret}`).toString('base64')}`;
    }
    const request = module.request({
      host: unbracket(proxy.url.hostname),
      port: defaultPort(proxy.url),
      method: 'CONNECT',
      path: authority,
      headers,
      signal,
    });
    request.on('socket', (socket) => sockets.add(socket));

    // THE STATUS IS CHECKED. Without this a refusal surfaces as a
    // bewildering TLS error about a handshake that never happened,
    // because the bytes of an HTTP error page are not a server hello.
    request.on('connect', (response, socket, head) => {
      sockets.add(socket);
      if (response.statusCode !== 200) {
        socket.destroy();
        reject(new Refused(proxyFailure(proxy,
          `it answered ${response.statusCode} to the request to open a tunnel`)));
        return;
      }
      if (head && head.length) {
        // Bytes the proxy handed back with the response belong to the
        // tunnel, and dropping them corrupts the first record of the
        // TLS handshake.
        socket.unshift(head);
      }
      resolve(socket);
    });
    request.on('response', (response) => {
      discard(response);
      reject(new Refused(proxyFailure(proxy,
        `it answered ${response.statusCode} to the request to open a tunnel`)));
    });
    request.on('error', (err) => reject(new Refused(proxyFailure(proxy,
      `the connection to it failed (${err.code || err.message})`))));
    request.end();
  });
}

// secureThrough wraps a tunnel in TLS for the real target.
//
// THE NAME IS NOT OPTIONAL AND IT IS NOT ALWAYS A NAME. Without a
// server name the handshake reaches a host that serves many and gets
// the wrong certificate, which fails in a way that looks like a network
// fault. But the standard library REFUSES an address literal as a
// server name — measured, not assumed — because the specification does
// not permit one, so a literal is passed as the host to check against
// instead, unbracketed, which is the spelling a certificate carries.
function secureThrough(tunnel, target, sockets) {
  return new Promise((resolve, reject) => {
    const host = unbracket(target.hostname);
    const options = { socket: tunnel, host };
    if (!net.isIP(host)) {
      options.servername = host;
    }
    const secure = tls.connect(options);
    sockets.add(secure);
    secure.once('secureConnect', () => resolve(secure));
    secure.once('error', reject);
  });
}

// sendGet issues one request and hands back the response headers. The
// body is left unread so the caller can decide whether to keep it.
//
// A TUNNELLED REQUEST NEEDS AN AGENT rather than a bare option: a
// client request with an agent asks the agent for its socket, and
// options.createConnection is only consulted when there is no agent at
// all — which `agent: false` does not produce, since it quietly makes a
// fresh one.
function sendGet(url, signal, sockets, socket) {
  return new Promise((resolve, reject) => {
    const module = url.protocol === 'https:' ? https : http;
    const options = {
      method: 'GET',
      signal,
      headers: { 'user-agent': `curiouspub/${pkg.version}` },
    };
    if (socket) {
      const agent = new https.Agent({ keepAlive: false, maxSockets: 1 });
      agent.createConnection = () => socket;
      options.agent = agent;
    }
    const request = module.request(url, options);
    request.on('socket', (s) => sockets.add(s));
    request.on('response', resolve);
    request.on('error', reject);
    request.end();
  });
}

// discard ends a response whose body this script is not going to read.
//
// DRAINING IS NOT CLOSING, and the difference is the whole point. A
// stream set flowing and thrown away is still a stream being read: a
// host that answers a redirect, or an error, with a body that never
// ends goes on sending it — down a socket nobody is going to look at —
// for as long as this process lives, while the download carries on at
// the next address. Destroying takes the connection with it, which is
// the only thing that actually stops a sender.
//
// It is called for every answer whose body is not wanted, on both
// paths. The tunnelled path had a socket of its own to destroy and the
// direct one had nothing, which is exactly the sort of difference that
// survives review because only half of it is visible in any one place.
function discard(response) {
  response.destroy();
}

// readBody collects a response, hashing as it goes, into a temp file in
// THIS directory.
//
// THE TEMP FILE IS HERE, AND UNIQUELY NAMED, for two separate reasons:
// a rename across devices fails, and two installs running at once are
// ordinary. It is closed before anything renames it, because on Windows
// renaming a file something still holds open fails outright — as does
// renaming one an antivirus scanner has not finished with.
function readBody(response) {
  return new Promise((resolve, reject) => {
    const file = path.join(
      __dirname, `.curious-download-${process.pid}-${crypto.randomBytes(6).toString('hex')}`);
    const hash = crypto.createHash('sha256');
    const out = fs.createWriteStream(file);
    let bytes = 0;

    // ONE OUTCOME, and cleanup that waits for the handle to go.
    //
    // Destroying the stream and removing the file in the next statement
    // is a race with the close. On Windows, removing a file something
    // still holds open fails outright with EPERM — and this runs inside
    // an event handler, so the throw does not become a failed install,
    // it escapes as an unexpected error and leaves BOTH the partial
    // temp file and a stack trace where the honest message should be.
    // That is precisely the "nothing left behind" promise failing, on
    // the one platform nobody writing this can try it on.
    //
    // So: wait for the stream's own close, then remove. And the flag,
    // because more than one thing can fail at once — a reset connection
    // ends the response and the write stream both — and the second
    // arrival must not turn a settled failure into a resolve, nor
    // report a different error than the first one.
    let settled = false;

    const fail = (err) => {
      if (settled) {
        return;
      }
      settled = true;
      const reason = err instanceof Retryable ? err : new Retryable(err.message);
      const discardFile = () => {
        try {
          fs.rmSync(file, { force: true });
        } catch {
          // A temp file that refuses to go is not a reason to replace
          // the real failure with a different one. The install still
          // stops, and it stops saying why it stopped.
        }
        reject(reason);
      };
      if (out.closed) {
        discardFile();
        return;
      }
      out.once('close', discardFile);
      out.destroy();
    };

    response.on('data', (chunk) => {
      hash.update(chunk);
      bytes += chunk.length;
    });
    response.on('error', fail);
    out.on('error', fail);
    response.pipe(out);
    out.on('close', () => {
      if (settled) {
        return;
      }
      if (!response.complete) {
        fail(new Retryable('the connection closed before the whole file arrived'));
        return;
      }
      settled = true;
      resolve({ file, digest: hash.digest('hex'), bytes });
    });
  });
}

// statusError decides whether an answer is worth asking again for.
function statusError(status, url) {
  const where = `${url.host}${url.pathname}`;
  if (status >= 500) {
    return new Retryable(`the release host answered ${status} for ${where}`);
  }
  return new Refused(
    `The release host answered ${status} for ${where}.\n\n` +
    (status === 404
      ? 'That version of curious has no build for this platform, which usually\n' +
        'means this package and the release have got out of step. Please report it.'
      : 'Nothing was downloaded.'));
}

// translate turns whatever the transport produced into something a
// person can read, and leaves anything already decided alone.
//
// THE CERTIFICATE FAMILY IS CALLED OUT BY NAME because the honest way
// through it is an environment variable most people have never heard
// of — and because an install that fails behind an inspecting proxy
// with no route forward is exactly how somebody ends up reaching for a
// flag that turns verification off.
function translate(err, signal) {
  if (err instanceof Refused || err instanceof Retryable) {
    return err;
  }
  if (!(err instanceof Error)) {
    return new Retryable(String(err));
  }
  if (signal.aborted && (err.name === 'AbortError' || err.code === 'ABORT_ERR')) {
    return new Retryable(`nothing arrived within ${ATTEMPT_TIMEOUT_MS / 1000} seconds`);
  }
  // OBSERVED SHAPES ONLY. The codes matched here were produced by real
  // failures against real servers while this was written; keying on a
  // code nobody has seen is how a branch that never runs gets written.
  if (typeof err.code === 'string' &&
      (err.code.includes('CERT') || err.code.startsWith('ERR_TLS'))) {
    return new Refused(
      'The TLS certificate the download host presented could not be verified:\n\n' +
      `  ${err.message}\n\n` +
      'If you are behind a proxy that inspects TLS, point NODE_EXTRA_CA_CERTS\n' +
      'at your organisation\'s certificate authority file and install again.\n' +
      'curious will not skip this check.');
  }
  return new Retryable(err.message);
}

// attemptFetch is ONE attempt: everything from the first request to the
// bytes on disk, under a single deadline.
async function attemptFetch(startURL) {
  const controller = new AbortController();
  const sockets = new Set();
  const timer = setTimeout(() => {
    controller.abort();
    // ABORTING THE REQUEST IS NOT ENOUGH ON ITS OWN. A timeout that
    // only stops waiting leaves the attempt's socket open, and a
    // tunnelled socket is not owned by any request at all.
    for (const socket of sockets) {
      socket.destroy();
    }
  }, ATTEMPT_TIMEOUT_MS);

  try {
    let url = new URL(startURL);
    for (let hop = 0; ; hop += 1) {
      checkScheme(url, hop === 0 ? 'The download address' : 'The download was redirected to what');

      // THE PROXY DECISION IS MADE AGAIN FOR EVERY HOP. A redirect that
      // crosses hosts leaves the previous tunnel serving the wrong
      // origin, and a host the rules exempt may sit at the other end of
      // one that does not.
      const proxy = proxyFor(url);
      const tunnel = proxy
        ? await secureThrough(await openTunnel(proxy, url, controller.signal, sockets), url, sockets)
        : null;
      const response = await sendGet(url, controller.signal, sockets, tunnel);
      const status = response.statusCode;

      if (status >= 300 && status < 400) {
        discard(response);
        if (tunnel) {
          tunnel.destroy();
        }
        if (hop >= MAX_REDIRECTS) {
          throw new Refused(
            `The download was redirected more than ${MAX_REDIRECTS} times, which\n` +
            'means it is going in a circle. Nothing was downloaded.');
        }
        const location = response.headers.location;
        if (!location) {
          throw new Refused(
            `The release host answered ${status} — a redirect — and said nothing\n` +
            'about where to. Nothing was downloaded, and this is not something\n' +
            'trying again will fix. Please report it.');
        }
        try {
          // Resolved against the address that sent it, so a bare path
          // is a redirect within the same host rather than a failure.
          url = new URL(location, url);
        } catch {
          throw new Refused(
            `The release host redirected the download to "${location}", which is\n` +
            'not an address curious can follow. Nothing was downloaded.');
        }
        continue;
      }

      if (status !== 200) {
        discard(response);
        throw statusError(status, url);
      }
      return await readBody(response);
    }
  } catch (err) {
    throw translate(err, controller.signal);
  } finally {
    clearTimeout(timer);
  }
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// download runs the attempts. A failure that is not worth repeating
// stops immediately; the rest wait and try again.
async function download(url) {
  let last;
  for (let attempt = 1; attempt <= ATTEMPTS; attempt += 1) {
    try {
      return await attemptFetch(url);
    } catch (err) {
      if (!(err instanceof Retryable)) {
        throw err;
      }
      last = err;
      if (attempt < ATTEMPTS) {
        await sleep(BACKOFF_MS[attempt - 1]);
      }
    }
  }
  throw new Refused(
    `curious could not download the binary after ${ATTEMPTS} attempts.\n\n` +
    `The last one ended because ${last.message}.\n\n` +
    'Check your connection and install again. Nothing was left behind.');
}

// ---------------------------------------------------------------------
// Steps 4 to 7: check it, unpack it, put it in place.
// ---------------------------------------------------------------------

function verify(downloaded, expected) {
  if (downloaded.digest === expected) {
    return;
  }
  fs.rmSync(downloaded.file, { force: true });
  throw new Refused(
    'The binary that arrived is not the one this package expected.\n\n' +
    `  expected  ${expected}\n` +
    `  received  ${downloaded.digest}\n\n` +
    'That is either a corrupted download or a tampered release asset, and\n' +
    'both deserve a stop rather than an install. Nothing has been written.\n' +
    'Please report it.');
}

// place decompresses the VERIFIED bytes into a second temp file, closes
// it, makes it executable, and renames it over whatever was there.
//
// THE ORDER MATTERS. Renaming the compressed file into place and
// decompressing afterwards would leave a window in which the binary's
// name holds something that is not a binary.
function place(downloaded, binary) {
  const target = path.join(__dirname, binary);
  const staged = path.join(
    __dirname, `.curious-staged-${process.pid}-${crypto.randomBytes(6).toString('hex')}`);
  try {
    const bytes = zlib.gunzipSync(fs.readFileSync(downloaded.file));
    fs.writeFileSync(staged, bytes);
    // 0755 rather than 0777: whoever installed it owns it, everyone
    // else may run it. A no-op on Windows, which has no such bits.
    fs.chmodSync(staged, 0o755);
    fs.renameSync(staged, target);
  } catch (err) {
    fs.rmSync(staged, { force: true });
    fs.rmSync(downloaded.file, { force: true });
    throw new Refused(
      `The downloaded binary could not be unpacked into place:\n\n  ${err.message}\n\n` +
      'Check that the directory this package was installed into is writable.');
  }
  fs.rmSync(downloaded.file, { force: true });
}

// The marker is how the shim knows the binary beside it belongs to THIS
// version of the package.
//
// IT IS REMOVED BEFORE ANY WORK AND WRITTEN AFTER ALL OF IT, so a
// failed upgrade leaves nothing claiming to be current. Without it a
// half-finished install in a reused directory leaves the previous
// release's binary sitting there under the right name, and the shim
// happily runs it.
const MARKER = 'installed.json';

function clearMarker() {
  fs.rmSync(path.join(__dirname, MARKER), { force: true });
}

function writeMarker(binary) {
  fs.writeFileSync(
    path.join(__dirname, MARKER),
    JSON.stringify({ version: pkg.version, binary }, null, 2) + '\n');
}

// ---------------------------------------------------------------------

async function main() {
  requireNodeFloor();
  const target = resolveTarget();
  const expected = expectedDigest(target.asset);
  clearMarker();
  const downloaded = await download(downloadURL(target.asset));
  verify(downloaded, expected);
  place(downloaded, target.binary);
  writeMarker(target.binary);
}

// THE ONE PLACE A FAILURE IS PRINTED, and it never prints a stack
// trace: a postinstall failure is read by somebody who typed one
// command and expected it to work, and a trace tells them about this
// file rather than about what to do next.
main().catch((err) => {
  const message = err instanceof Refused
    ? err.message
    : `Something went wrong that this installer did not expect:\n\n  ${err && err.message}\n\n` +
      'Please report it.';
  console.error(`\ncurious could not be installed.\n\n${message}\n`);
  process.exit(1);
});
