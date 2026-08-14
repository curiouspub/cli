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

What lands here next:

- **`curious deploy`** — pack an Astro project, upload it, watch the
  build logs stream live, get a `*.curiously.dev` URL.
- **`curious mcp`** — the same flow as a stdio MCP server, so Claude
  Code, Claude Desktop, and other MCP clients can deploy for you.

The trial opens in small daily batches. Get the launch note:
[hello -a- curious.pub]

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

Types for capacity, waitlist, and auth are present; deploy types land
with the deploy endpoints. Success is signalled by HTTP status alone;
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
