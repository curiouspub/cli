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

1. `GET /v1/capacity` FIRST. Closed → offer waitlist, stop. Never let
   a user (or agent) do work that can't land.
2. Token from `~/.config/curious/config.json`; absent → login flow.
3. Pre-flight (below). 4. Pack. 5. Create deploy + PUT tarball.
6. Start + stream SSE. 7. Print URL + expiry.

Login: email → 6-digit code (10-min expiry). Wrong/expired code NEVER
restarts the flow — offer "Resend code? [Y/n]" in a retry loop.
Marketing consent is a separate prompt, default No:
`Product news a few times a year? [y/N]`. Transactional wording only
elsewhere.

## Pre-flight checks (fail-fast, before any network write)

| check | severity |
|---|---|
| `astro` in package.json dependencies | HARD STOP |
| lockfile present (package-lock.json / pnpm-lock.yaml) | HARD STOP — builder runs `npm ci` |
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
