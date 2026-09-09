# curiouspub

The npm wrapper for [`curious`](https://github.com/curiouspub/cli) — the
command line tool for [curious.pub](https://curious.pub).

**Your agent builds. You approve. It ships.**

```
npx curiouspub deploy
```

The package name is `curiouspub`; the command it installs is `curious`.
They are different names on purpose — `npx curiouspub` is the
run-without-installing form, and `curious` is what you type once it is
installed.

## What the install does

This package contains no binary. Its postinstall script downloads the
release build for your platform, checks it, and puts it beside itself.

Supported: macOS, Linux and Windows, on x64 and arm64. Anything else
fails the install with a message saying so, rather than succeeding and
leaving you with a command that is not there.

Node 22 or newer. The install script checks that before it does
anything else, because a version check that runs after the download has
let the thing it was guarding already happen.

## How the download is checked, and what that is worth

A postinstall script that downloads and runs a binary is, mechanically,
what a malicious package does. The difference is entirely in what
vouches for the bytes, so it is worth stating exactly:

- **The digest is inside this package.** `checksums.json` is written
  when the package is published, from the same run that built the
  binaries, and the download is refused unless it matches. So a release
  asset **replaced after publication** fails the check — the digest is
  vouched for by the registry, not by the host serving the download.
- **That is the whole claim.** It assumes this npm package is itself
  authentic, and it says nothing about a compromised publisher or a
  compromised release run: a run that builds a binary and its checksum
  together will vouch for its own output. That residual is not closable
  by anything a dependency-free install script can do, and this package
  does not pretend otherwise.
- **A missing digest is refused too**, and it is not the same thing as a
  mismatch: it means the package was built wrong, and it stops before
  anything is downloaded.
- **The release is also signed** over its checksum file, keylessly, by
  the workflow that produced it. That signature is there for an auditor
  with the verifying tool. This script does not check it and does not
  claim to.
- **npm provenance** ties the published package to the workflow that
  built it. That is checkable on the registry page.

## Zero dependencies, and why

The install script uses Node's standard library and nothing else. That
is part of the argument rather than a preference: a reader can audit the
whole of `install.js` in one sitting, which is the only real answer to
"why should I let this run on my machine".

The same reasoning is why the proxy support is a hand-written tunnel
rather than a package.

## Behind a proxy

`npm_config_https_proxy` and `npm_config_proxy` are read first, then
`HTTPS_PROXY` and `https_proxy`; `npm_config_no_proxy` and `NO_PROXY`
are honoured, including the `*` and leading-dot forms.

A bypass entry names **one host**, and the leading dot is what widens
it: `example.com` covers that host alone, while `.example.com` covers it
and everything under it. An entry may carry a port, and then it matches
only that port. The distinction decides between a tunnel and a direct
connection, so it errs towards the proxy: an entry that matched more
hosts than it names would send the download direct on a machine whose
policy says tunnel.

If a proxy is configured and the tunnel fails, **the install fails and
names the proxy**. It never quietly connects directly instead: on a
machine where direct access is blocked that would be a bypass nobody
asked for.

If your proxy inspects TLS, point `NODE_EXTRA_CA_CERTS` at your
organisation's certificate authority file. That is the supported route,
and it is the only one — nothing in this package can turn certificate
verification off.

## If the install did not run

Installing with `--ignore-scripts` skips the download, and the command
then says so rather than failing with a stack trace. Install again
without it, or take a binary from the
[releases page](https://github.com/curiouspub/cli/releases).

## No telemetry

None. No analytics, no phone-home, no auto-update, no lifecycle script
other than the postinstall. The tool talks to the API you point it at
and to nothing else.

## License

MIT
