'use strict';

// The throwaway certificates the TLS rows run on, and the encoding rule
// that decides whether a verifier will look at them at all.
//
// THIS FILE EXISTS BECAUSE THE SUITE WAS FLAKY AND THE CAUSE WAS HERE.
// Roughly one run in ten reddened with "illegal padding" raised inside
// https.createServer, in whichever row happened to mint the offending
// certificate — a different row each time, which is exactly what a
// per-certificate probability looks like from the outside. The rows
// below drive the shape that produced it rather than waiting for random
// bytes to produce it again: a flake reproduced only by repetition is
// one nobody can prove they fixed.

const test = require('node:test');
const assert = require('node:assert');
const crypto = require('node:crypto');
const https = require('node:https');

const { authority, derInteger } = require('./helpers/certs');

// The serial that used to break the suite: a first byte of zero in front
// of a byte whose top bit is clear. Random bytes land on it about once in
// every two hundred and fifty-six certificates.
const ZERO_LED_SERIAL = Buffer.from([0x00, 0x22, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66]);

// The other direction, and the one a naive "strip leading zeros" would
// break: the top bit is set, so the value needs a zero in front of it or
// it reads as negative.
const HIGH_BIT_SERIAL = Buffer.from([0x80, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77]);

// withSerial makes the minting deterministic. The helper takes its serial
// from the one call to randomBytes it makes, so replacing that call fixes
// the certificate this row is about instead of waiting for chance to
// deal it.
function withSerial(bytes, body) {
  const real = crypto.randomBytes;
  crypto.randomBytes = (n) => (n === bytes.length ? Buffer.from(bytes) : real(n));
  try {
    return body();
  } finally {
    crypto.randomBytes = real;
  }
}

test('a der integer is written in the one form a verifier will accept', () => {
  // THE ABSENCE. A zero in front of a byte whose top bit is clear says
  // nothing about the value and makes the encoding refusable.
  assert.deepStrictEqual(
    [...derInteger(Buffer.from([0x00, 0x22]))], [0x02, 0x01, 0x22]);
  assert.deepStrictEqual(
    [...derInteger(Buffer.from([0x00, 0x00, 0x01]))], [0x02, 0x01, 0x01]);

  // THE PRESENCE, and it is the half a blanket strip would break. A
  // leading zero in front of a byte whose top bit IS set is the sign,
  // and it has to be there.
  assert.deepStrictEqual(
    [...derInteger(Buffer.from([0x00, 0x80]))], [0x02, 0x02, 0x00, 0x80]);
  assert.deepStrictEqual(
    [...derInteger(Buffer.from([0x80, 0x11]))], [0x02, 0x03, 0x00, 0x80, 0x11]);

  // Zero is one byte rather than none.
  assert.deepStrictEqual([...derInteger(Buffer.from([0x00, 0x00]))], [0x02, 0x01, 0x00]);
});

test('a serial that begins with a zero byte still makes a certificate a TLS server loads', () => {
  const ca = withSerial(ZERO_LED_SERIAL, () => authority('zero-led-serial'));

  // The failure this reproduces was raised HERE, loading the chain into
  // a server, and never where the certificate was made.
  const server = https.createServer({ cert: ca.certPem, key: ca.keyPem });
  server.close();

  // And the certificate carries the value it was given, with the
  // meaningless byte gone rather than the number changed.
  const leaf = new crypto.X509Certificate(ca.certPem);
  assert.strictEqual(leaf.serialNumber, '22112233445566');
  const root = new crypto.X509Certificate(ca.caPem);
  assert.strictEqual(root.serialNumber, '22112233445566');
});

test('a serial whose top bit is set keeps the zero that makes it positive', () => {
  const ca = withSerial(HIGH_BIT_SERIAL, () => authority('high-bit-serial'));

  const server = https.createServer({ cert: ca.certPem, key: ca.keyPem });
  server.close();

  // Read back as a positive number: without the sign byte this would be
  // a negative serial, which is a different certificate and a refusable
  // one.
  const leaf = new crypto.X509Certificate(ca.certPem);
  assert.strictEqual(leaf.serialNumber, '8011223344556677');
});
