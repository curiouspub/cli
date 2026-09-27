#!/usr/bin/env node
'use strict';

// Fetch the notary service's own log for each macOS binary this release
// run submitted, and write them where the workflow uploads them as a run
// artefact.
//
// WHY THIS IS A SEPARATE STEP. The release tool fetches that log only when
// a submission FAILS, and puts it in the error. A submission that passes
// prints one line and no identifier, so without this step a release run
// holds no record of what the notary service checked.
//
// WHAT IT READS. The service lists recent submissions; the ones this run
// made are the ones named after the binary and created after the moment
// the workflow recorded just before the build. Each one's log is behind a
// presigned link, which is a credential for as long as it lives, so it
// is followed and never printed.
//
// Inputs, all from the environment: the API key (base64 of the .p8 file,
// as the release tool reads it), its id, the issuer id, and NOTARY_SINCE.
// The one argument is the directory to write into.

const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');

const api = 'https://appstoreconnect.apple.com/notary/v2';
const out = process.argv[2];

function stop(message) {
  console.error(`notary-log: ${message}`);
  process.exit(1);
}

if (!out) {
  stop('usage: scripts/notary-log.js <output directory>');
}
for (const name of ['MACOS_NOTARY_KEY', 'MACOS_NOTARY_KEY_ID', 'MACOS_NOTARY_ISSUER_ID', 'NOTARY_SINCE']) {
  if (!process.env[name]) {
    stop(`${name} is empty`);
  }
}
const since = Date.parse(process.env.NOTARY_SINCE);
if (Number.isNaN(since)) {
  stop('NOTARY_SINCE is not a timestamp');
}

// The notary service's token: ES256 over the key id, the issuer and a
// lifetime well inside the twenty minutes it accepts.
function token() {
  const b64url = (value) => Buffer.from(value).toString('base64url');
  const now = Math.floor(Date.now() / 1000);
  const header = b64url(JSON.stringify({ alg: 'ES256', kid: process.env.MACOS_NOTARY_KEY_ID, typ: 'JWT' }));
  const claims = b64url(JSON.stringify({
    iss: process.env.MACOS_NOTARY_ISSUER_ID,
    iat: now,
    exp: now + 600,
    aud: 'appstoreconnect-v1',
    scope: ['/notary/v2'],
  }));
  const key = crypto.createPrivateKey(Buffer.from(process.env.MACOS_NOTARY_KEY, 'base64').toString('utf8'));
  const signature = crypto.sign('sha256', Buffer.from(`${header}.${claims}`), { key, dsaEncoding: 'ieee-p1363' });
  return `${header}.${claims}.${signature.toString('base64url')}`;
}

async function read(url, auth) {
  const response = await fetch(url, auth ? { headers: { Authorization: `Bearer ${auth}` } } : {});
  if (!response.ok) {
    // The status only. A body from this service can echo the request,
    // and one of the requests made here is a presigned link.
    throw new Error(`the notary service answered ${response.status}`);
  }
  return response;
}

async function main() {
  const auth = token();
  const listing = await (await read(`${api}/submissions`, auth)).json();
  const ours = (listing.data || []).filter((s) =>
    s.attributes && s.attributes.name === 'curious' && Date.parse(s.attributes.createdDate) >= since);
  if (ours.length === 0) {
    stop(`the notary service lists no submission named curious since ${process.env.NOTARY_SINCE}`);
  }

  fs.mkdirSync(out, { recursive: true });
  for (const submission of ours) {
    const meta = await (await read(`${api}/submissions/${submission.id}/logs`, auth)).json();
    const link = meta && meta.data && meta.data.attributes && meta.data.attributes.developerLogUrl;
    if (!link) {
      stop(`submission ${submission.id} has no log yet`);
    }
    const log = await (await read(link)).text();
    fs.writeFileSync(path.join(out, `${submission.id}.json`), log);
    let summary = '';
    try {
      const parsed = JSON.parse(log);
      summary = `, ${parsed.status}, ${(parsed.issues || []).length} issue(s), archive ${parsed.archiveFilename}`;
    } catch {
      summary = ', a log that is not JSON';
    }
    console.log(`notary-log: ${submission.id} (${submission.attributes.status}, created ${submission.attributes.createdDate})${summary}`);
  }
  console.log(`notary-log: ${ours.length} log(s) written to ${out}`);
}

main().catch((err) => stop(err.message));
