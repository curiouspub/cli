'use strict';

// TEMPORARY, AND ITS REMOVAL IS PART OF THE FIX. Two CI legs failed on
// values nobody here can read: a Windows runner and a Node patch. This
// file prints those values from inside the legs, so the fix is written
// against what the machine reports rather than against what the author
// believes the machine would report.

const test = require('node:test');
const fs = require('node:fs');
const path = require('node:path');
const net = require('node:net');
const tls = require('node:tls');
const nodeUrl = require('node:url');
const { X509Certificate } = require('node:crypto');
const { execFile } = require('node:child_process');

const h = require('./helpers/harness');

function say(...parts) {
  console.log('DIAG', ...parts);
}

function once(command, args, options = {}) {
  return new Promise((resolve) => {
    execFile(command, args, { encoding: 'utf8', maxBuffer: 1 << 24, ...options },
      (err, stdout, stderr) => resolve({ err, stdout, stderr }));
  });
}

test('DIAG the leg', () => {
  say('version', process.version, 'platform', process.platform, 'arch', process.arch);
  say('execPath', JSON.stringify(process.execPath));
  say('sep', JSON.stringify(path.sep), 'cwd', JSON.stringify(process.cwd()));
});

test('DIAG what the identity check does with an address literal', () => {
  const ca = h.authority('diag');
  const end = '-----END CERTIFICATE-----';
  const leaf = ca.certPem.slice(0, ca.certPem.indexOf(end) + end.length) + '\n';
  const x = new X509Certificate(leaf);
  say('subjectAltName', JSON.stringify(x.subjectAltName));

  for (const host of ['::1', '0:0:0:0:0:0:0:1', '[::1]', '127.0.0.1']) {
    const ascii = nodeUrl.domainToASCII(host);
    const cert = { subject: { CN: 'localhost' }, subjectaltname: x.subjectAltName, raw: x.raw };
    let verdict;
    try {
      verdict = tls.checkServerIdentity(host, cert);
    } catch (err) {
      verdict = err;
    }
    let byIP;
    try {
      byIP = x.checkIP(host);
    } catch (err) {
      byIP = `threw ${err.code || err.message}`;
    }
    say('host', JSON.stringify(host),
      '| net.isIP', net.isIP(host),
      '| domainToASCII', JSON.stringify(ascii),
      '| net.isIP(ascii)', net.isIP(ascii),
      '| checkServerIdentity', verdict ? verdict.message : 'ACCEPTS',
      '| X509#checkIP', JSON.stringify(byIP));
  }
});

test('DIAG how npm can be run from here', async () => {
  const beside = path.dirname(process.execPath);
  const candidates = [
    path.join(beside, 'node_modules', 'npm', 'bin', 'npm-cli.js'),
    path.join(beside, '..', 'lib', 'node_modules', 'npm', 'bin', 'npm-cli.js'),
  ];
  for (const candidate of candidates) {
    say('candidate', JSON.stringify(candidate), fs.existsSync(candidate) ? 'EXISTS' : 'absent');
  }
  say('npm_execpath', JSON.stringify(process.env.npm_execpath || null));
  for (const entry of (process.env.PATH || '').split(path.delimiter)) {
    for (const name of ['npm', 'npm.cmd', 'npm.exe']) {
      const full = path.join(entry, name);
      if (fs.existsSync(full)) {
        say('on PATH', JSON.stringify(full));
      }
    }
  }

  const direct = await once('npm', ['--version']);
  if (direct.err) {
    const e = direct.err;
    say('execFile npm REFUSED',
      '| code', JSON.stringify(e.code),
      '| errno', JSON.stringify(e.errno),
      '| syscall', JSON.stringify(e.syscall),
      '| path', JSON.stringify(e.path),
      '| spawnargs', JSON.stringify(e.spawnargs),
      '| message', JSON.stringify(e.message));
  } else {
    say('execFile npm ok', JSON.stringify(direct.stdout.trim()));
  }

  let onPath = null;
  for (const entry of (process.env.PATH || '').split(path.delimiter)) {
    const full = path.join(entry, 'npm');
    try {
      const real = fs.realpathSync(full);
      say('realpath of', JSON.stringify(full), '->', JSON.stringify(real));
      if (!onPath && real.endsWith('.js')) {
        onPath = real;
      }
    } catch {
      // Not on this entry.
    }
  }

  const withShell = await once('npm', ['--version'], { shell: true });
  say('execFile npm shell:true ->', withShell.err
    ? `REFUSED ${JSON.stringify(withShell.err.code)}`
    : JSON.stringify(withShell.stdout.trim()));

  const cli = onPath || candidates.find((candidate) => fs.existsSync(candidate));
  say('chosen npm-cli.js', JSON.stringify(cli));
  if (!cli) {
    say('nothing resolved to an npm-cli.js — nothing further to try');
    return;
  }
  const packed = await once(process.execPath, [cli, 'pack', '--dry-run', '--json'],
    { cwd: h.PACKAGE_ROOT });
  if (packed.err) {
    say('node npm-cli.js pack REFUSED', JSON.stringify(packed.err.message),
      JSON.stringify(packed.stderr.slice(0, 600)));
    return;
  }
  const [report] = JSON.parse(packed.stdout);
  say('pack paths', JSON.stringify(report.files.map((f) => f.path).sort()));
  say('pack modes', JSON.stringify(report.files.map((f) => [f.path, f.mode])));
});
