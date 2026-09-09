'use strict';

// A throwaway certificate authority and the leaf certificates the TLS
// rows need, MINTED at test time rather than committed.
//
// WHY NOTHING IS COMMITTED, and it is a measurement rather than a
// preference:
//
//   1. A private key committed to a world-readable repository is a
//      private key committed to a world-readable repository, whatever
//      the comment beside it says. Nobody diffing this tree should have
//      to decide which of them is inert.
//   2. This repository's own vocabulary guard reads subwords inside
//      identifiers, and base64 spells capitalised fragments freely. It
//      was measured against real certificate material before this file
//      was written: 627 lines of PEM produced six refusals, about one
//      line in a hundred. A four-certificate fixture is roughly 120
//      lines, so committing one is a two-in-three chance of a red build
//      on bytes no author chose — and the only quick way out of that
//      red is loosening the guard, which is the guard weakened by the
//      thing that trips it.
//
// The standard library will generate a key pair and sign with it, and
// it will parse an X.509 certificate, but it will not ISSUE one. So the
// encoder below writes the certificate by hand. It is small on purpose:
// it emits exactly the fields a verifier needs to accept a chain of two
// for a loopback name, and nothing else.

const crypto = require('node:crypto');

// ---------------------------------------------------------------------
// The smallest DER encoder that can write a certificate.
// ---------------------------------------------------------------------

function length(n) {
  if (n < 0x80) {
    return Buffer.from([n]);
  }
  const bytes = [];
  for (let rest = n; rest > 0; rest = Math.floor(rest / 256)) {
    bytes.unshift(rest % 256);
  }
  return Buffer.from([0x80 | bytes.length, ...bytes]);
}

function tlv(tag, contents) {
  return Buffer.concat([Buffer.from([tag]), length(contents.length), contents]);
}

const seq = (...parts) => tlv(0x30, Buffer.concat(parts));
const set = (...parts) => tlv(0x31, Buffer.concat(parts));
const bool = (v) => tlv(0x01, Buffer.from([v ? 0xff : 0x00]));
const utf8 = (s) => tlv(0x0c, Buffer.from(s, 'utf8'));
const ia5 = (s) => tlv(0x16, Buffer.from(s, 'ascii'));
const octets = (b) => tlv(0x04, b);
const explicit = (n, contents) => tlv(0xa0 | n, contents);

// A BIT STRING carries a count of unused trailing bits. Everything here
// is whole bytes, so the count is always zero.
const bits = (b) => tlv(0x03, Buffer.concat([Buffer.from([0]), b]));

// A DER integer is signed AND minimal, and both halves are load-bearing
// here because one of the values below is eight random bytes.
//
// Signed: a leading byte with its high bit set has to be padded with a
// zero or the value reads as negative.
//
// MINIMAL: a leading zero byte is permitted ONLY when the byte after it
// has its high bit set. A zero in front of a byte that does not is a
// value written longer than it needs to be, and a real verifier refuses
// the whole certificate for it — "illegal padding", raised where the
// certificate is loaded rather than where it was made.
//
// That is not a theoretical branch. It is what a random serial produces
// whenever its first byte is zero and its second is under 0x80, which is
// about one certificate in every two hundred and fifty-six: rare enough
// to read as an unrelated flake in whichever row happened to mint it,
// common enough to redden a suite that makes a couple of dozen.
function integer(buf) {
  let start = 0;
  while (start + 1 < buf.length && buf[start] === 0 && (buf[start + 1] & 0x80) === 0) {
    start += 1;
  }
  const trimmed = buf.subarray(start);
  const body = trimmed[0] & 0x80
    ? Buffer.concat([Buffer.from([0]), trimmed])
    : trimmed;
  return tlv(0x02, Buffer.from(body));
}

function oid(dotted) {
  const parts = dotted.split('.').map(Number);
  const bytes = [parts[0] * 40 + parts[1]];
  for (const part of parts.slice(2)) {
    const chunk = [part & 0x7f];
    for (let rest = Math.floor(part / 128); rest > 0; rest = Math.floor(rest / 128)) {
      chunk.unshift((rest & 0x7f) | 0x80);
    }
    bytes.push(...chunk);
  }
  return tlv(0x06, Buffer.from(bytes));
}

function utcTime(date) {
  const pad = (n) => String(n).padStart(2, '0');
  const text =
    pad(date.getUTCFullYear() % 100) +
    pad(date.getUTCMonth() + 1) +
    pad(date.getUTCDate()) +
    pad(date.getUTCHours()) +
    pad(date.getUTCMinutes()) +
    pad(date.getUTCSeconds()) +
    'Z';
  return tlv(0x17, Buffer.from(text, 'ascii'));
}

// ---------------------------------------------------------------------
// The certificate itself.
// ---------------------------------------------------------------------

// ecdsa-with-SHA256. The signature the standard library produces for an
// elliptic-curve key is already the DER sequence a certificate wants.
const ECDSA_SHA256 = seq(oid('1.2.840.10045.4.3.2'));

const COMMON_NAME = oid('2.5.4.3');
const BASIC_CONSTRAINTS = oid('2.5.29.19');
const SUBJECT_ALT_NAME = oid('2.5.29.17');
const EXTENDED_KEY_USAGE = oid('2.5.29.37');
const SERVER_AUTH = oid('1.3.6.1.5.5.7.3.1');

function name(commonName) {
  return seq(set(seq(COMMON_NAME, utf8(commonName))));
}

function extension(id, critical, value) {
  return critical
    ? seq(id, bool(true), octets(value))
    : seq(id, octets(value));
}

// generalNames encodes the loopback spellings a server certificate has
// to carry. A name and an address are different tags, and a verifier
// that is given only the first will refuse a connection dialled by the
// second — which is the failure that reads as a broken test rather than
// as a missing entry.
function generalNames(hosts) {
  const parts = hosts.map((host) => {
    if (host === '127.0.0.1') {
      return tlv(0x87, Buffer.from([127, 0, 0, 1]));
    }
    if (host === '::1') {
      const address = Buffer.alloc(16);
      address[15] = 1;
      return tlv(0x87, address);
    }
    return tlv(0x82, Buffer.from(host, 'ascii'));
  });
  return seq(...parts);
}

function newKeyPair() {
  return crypto.generateKeyPairSync('ec', { namedCurve: 'prime256v1' });
}

function pem(label, der) {
  const body = der.toString('base64').replace(/(.{64})/g, '$1\n').replace(/\n$/, '');
  return `-----BEGIN ${label}-----\n${body}\n-----END ${label}-----\n`;
}

function issue({ subject, issuer, subjectKey, issuerKey, isCA, hosts }) {
  const now = Date.now();
  const extensions = [
    extension(BASIC_CONSTRAINTS, true, isCA ? seq(bool(true)) : seq()),
  ];
  if (!isCA) {
    extensions.push(extension(SUBJECT_ALT_NAME, false, generalNames(hosts)));
    extensions.push(extension(EXTENDED_KEY_USAGE, false, seq(SERVER_AUTH)));
  }

  const tbs = seq(
    explicit(0, integer(Buffer.from([2]))),
    // The serial is handed over as it comes. Clearing the top bit here
    // was an attempt to keep the value positive that only moved the
    // problem: it turns a first byte of 0x00 or 0x80 into a zero the
    // encoder then had no reason to remove. Both the sign and the
    // shortest form belong to the encoder, which knows about the byte
    // that follows.
    integer(crypto.randomBytes(8)),
    ECDSA_SHA256,
    name(issuer),
    // An hour behind and a day ahead. A certificate that is only valid
    // from "now" loses to a clock a second slow on the other side.
    seq(utcTime(new Date(now - 3600e3)), utcTime(new Date(now + 86400e3))),
    name(subject),
    subjectKey.publicKey.export({ type: 'spki', format: 'der' }),
    explicit(3, seq(...extensions)));

  const signature = crypto.sign('sha256', tbs, issuerKey.privateKey);
  return pem('CERTIFICATE', seq(tbs, ECDSA_SHA256, bits(signature)));
}

// authority mints a root and one leaf under it. Two calls give two
// authorities, which is what the untrusted-certificate row needs: a
// server presenting a chain nobody told the client about.
function authority(label) {
  const rootKey = newKeyPair();
  const leafKey = newKeyPair();
  const rootName = `curious test root ${label}`;
  const root = issue({
    subject: rootName,
    issuer: rootName,
    subjectKey: rootKey,
    issuerKey: rootKey,
    isCA: true,
  });
  const leaf = issue({
    subject: 'localhost',
    issuer: rootName,
    subjectKey: leafKey,
    issuerKey: rootKey,
    isCA: false,
    hosts: ['localhost', '127.0.0.1', '::1'],
  });
  return {
    caPem: root,
    // The chain, leaf first: a client that trusts the root can build a
    // path to the leaf without fetching anything.
    certPem: leaf + root,
    keyPem: leafKey.privateKey.export({ type: 'pkcs8', format: 'pem' }),
  };
}

// derInteger is exported for the row that pins the encoding rule. It is
// the one piece of this file a caller cannot observe through a finished
// certificate: a verifier either accepts the whole thing or refuses it,
// and the bytes that decided which are the ones worth asserting on.
module.exports = { authority, derInteger: integer };
