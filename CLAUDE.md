# curiouspub/cli — `curious` CLI + local MCP server

PUBLIC repo, MIT. This is the open, inspectable half of curious.pub:
one Go binary, two faces. Everything here is world-readable — write
code and comments accordingly, and never reference internal
infrastructure (ARNs, bucket names, instance details).

## What this binary is

- `curious deploy [dir]` — interactive CLI: pack an Astro project,
  upload, stream build logs, print the live `*.curiously.dev` URL.
- `curious mcp` — stdio MCP server exposing the same flow to agents
  (Claude Code, Claude Desktop, etc.). A remote MCP server cannot read
  the user's disk; local stdio is the deliberate design.
- npm package `curiouspub` (thin wrapper, postinstall fetches the
  GoReleaser-built binary); binary name stays `curious`.

## Layout

- `cmd/curious/` — entrypoint, mode dispatch.
- `internal/` — packing, pre-flight, SSE client, config, UI.
- `pkg/wire/` — THE public wire contract for `/v1`. platform imports
  this module. Additive changes only; renaming/removing a released
  field is forbidden. Golden tests required.

## Client flow (order is intentional)

**Local truths before global state: a project that can't deploy makes
zero network calls.**

1. Token from `~/.config/curious/config.json` (no network).
2. Pre-flight (below). Hard stops abort here, having contacted nothing.
3. Scan + limits (below).
4. **Only if there's no usable token**: `GET /v1/capacity` — closed →
   offer waitlist, stop — then the login flow. Never let a user (or
   agent) do work that can't land. A user who already holds a token
   isn't gated: the daily cap counts new ACCOUNTS, spent at
   `/v1/auth/verify`, and their deploy spends none of it.
5. Pack. 6. Create deploy + PUT tarball. 7. Start + stream SSE.
8. Print URL + expiry.

Login: email → 6-digit code (10-min expiry). Wrong/expired code NEVER
restarts the flow — offer "Resend code? [Y/n]" in a retry loop.
Marketing consent is a separate prompt, default No:
`Product news a few times a year? [y/N]`. Transactional wording only
elsewhere.

## Pre-flight checks (fail-fast, before any network write)

| check | severity |
|---|---|
| `astro` in package.json dependencies | HARD STOP |
| lockfile present (package-lock.json / pnpm-lock.yaml) | HARD STOP — both accepted; the builder detects which package manager to install with. Skip if there's no package.json |
| pages dir exists — check `src/pages/`, and parse astro.config for custom `srcDir` before failing | HARD STOP (config-aware) |
| `http://localhost` in .astro/.js sources | WARNING — CLI: "continue? [Y/n]"; MCP: non-blocking warning in result |

Packing: respect `.gitignore`; always exclude `node_modules/`, `dist/`,
`.git/`, `.astro/`, `.env*`. Limits (local blockers, mirrored
server-side): ≤ 3,000 files, ≤ 5 MB per file, ≤ 30 MB total.

## MCP tools

`login_start(email)`, `login_verify(code, marketing_opt_in=false)`,
`deploy_site(path=cwd)` (MCP progress notifications while building;
returns `{url, expires_at, deploy_id}`), `deploy_status(deploy_id)`,
`whoami()`. Tool descriptions must state limits, quotas, and TTL so
agents self-serve errors instead of retrying blindly. Warnings never
block in MCP mode; they attach to the result.

## Hard don'ts

- No telemetry, analytics, or phone-home of any kind. Ever. This
  repo's existence is a trust argument; one tracker destroys it.
- No AWS SDK dependency; the client speaks only the `/v1` HTTP API.
- No server-side logic; if a validation matters for security, it's
  platform's job (ours is UX).
- Never print tokens, presigned URLs, or full config to the terminal
  or MCP output.
- Don't invent endpoints or fields — `pkg/wire` is the contract.

## Releases

GoReleaser: cross-platform binaries, checksums, GitHub Releases,
Homebrew tap; npm wrapper publish is a pipeline step. Version via tags,
semver. Error messages are part of the product — write them for a
clumsy first-timer.
