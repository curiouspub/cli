# curious

**Your agent builds. You approve. It ships.**

This is the open half of [curious.pub](https://curious.pub), an
agent-native publishing platform for Astro sites. Everything that runs
on your machine lives in this repository, inspectable end to end: the
`curious` command line tool, the local MCP server your AI agent talks
to, and the wire protocol that connects them to the platform.

The control plane is closed; the contract between us is not. If you
want to know exactly what the client sends and receives, read
[`pkg/wire`](./pkg/wire). That's the point of this repo existing in
public.

## Status: pre-release

The platform is not yet open. What exists here today:

- **`pkg/wire`** — the `/v1` wire contract (types only, stdlib-only,
  importable as `github.com/curiouspub/cli/pkg/wire`).
- **`curious mcp`** — the stdio MCP server, as transport and dispatch:
  it completes a handshake, lists tools and calls them. It registers no
  tools yet, so a client that connects today finds an empty list. Written
  against the standard library alone — no SDK, no new dependency.
- **`curious deploy`** — it checks your project, logs you in if it has
  to, packs the archive, uploads it, starts the build and streams the
  build log to your terminal until the build finishes. It stops there
  and says so, because the site's own address is not built yet.
  Everything it can answer without the network it answers first, so a
  project that cannot deploy never sends a byte. The build log goes to
  stdout and everything the tool says about it goes to stderr, so
  redirecting stdout collects the log and nothing else — and every line
  is escaped on the way out, because a build log is arbitrary program
  output and your terminal obeys some of it.

What lands here next:

- **The rest of `curious deploy`** — publish the built site and print
  its `*.curiously.dev` URL.
- **The MCP tools** — the same flow through `curious mcp`, so Claude
  Code, Claude Desktop, and other MCP clients can deploy for you.

The trial opens in small daily batches. Get the launch note:
[hello -a- curious.pub]

## Installing it

**Nothing is published yet.** There is no release and no package on any
registry, so the commands below are what installing will look like
rather than what works today. This section says so out loud because a
repository describing unbuilt things in the present tense has told its
reader something false.

What is in the tree now is the npm wrapper, under
[`npm/`](./npm) — a package whose postinstall fetches the release build
for your platform. Its own [README](./npm/README.md) has the detail.

```
npx curiouspub deploy       # run it without installing
npm install -g curiouspub   # install the `curious` command
```

The package is `curiouspub` and the command is `curious`. They are
different names on purpose.

**How the download is checked, and what that is worth.** A postinstall
script that downloads and runs a binary is, mechanically, what a
malicious package does, so it is worth being precise about what vouches
for the bytes. The SHA-256 digest lives inside the npm package, written
at publish time from the same run that built the binaries — so a release
asset **replaced after publication** fails the check, because the digest
is vouched for by the registry rather than by the host serving the
download. That is the whole claim: it assumes the npm package is itself
authentic, and it says nothing about a compromised publisher or a
compromised release run, which is not something a dependency-free
install script can close. The release is also signed over its checksum
file, for an auditor with the verifying tool; the install script does
not check that and does not pretend to.

The install script has **no dependencies at all**, which is part of the
argument rather than a preference: you can read the whole of it in one
sitting.

## Principles

**Zero telemetry.** No analytics, no phone-home, no exceptions. The
tool authenticates to the API you tell it to talk to, and that is the
only network traffic it creates beyond your deploys.

**The wire contract is additive-only.** Released clients keep working,
forever. Within `/v1`, fields and endpoints are added, never renamed,
retyped, or removed. The contract-guard tests in `pkg/wire` enforce
this mechanically; every field that exists carries meaning.

**Fail fast, fail local.** The CLI checks your project before a single
byte leaves your machine, and its error messages are written for a
first-timer, not a compiler.

## Using the wire types

```go
import "github.com/curiouspub/cli/pkg/wire"
```

Types for capacity, waitlist, auth and deploy are present. Success is
signalled by HTTP status alone;
error semantics live in the error envelope, and success-body contents
are never something to branch on.

## Contributing

Early days and a small team, so issues are welcome, PRs may wait, and
the protocol itself changes only through the platform's design process.
If you've found a security issue, mail
[abuse -a- curious.pub] instead of opening an
issue.

## License

[MIT](./LICENSE)

*Published curiously.*
