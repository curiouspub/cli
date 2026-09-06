# curiouspub/cli — the `curious` CLI and its local MCP server

PUBLIC repo, MIT. The open, inspectable half of curious.pub: one Go
binary with two faces — a terminal CLI and a local MCP server — so a
person and an agent deploy the same way.

Everything here is world-readable. Write code and comments accordingly,
and never reference internal infrastructure: no ARNs, no bucket names,
no instance details.

**This file is the public half of two.** Delivery orchestration — the
schedule, the review bookkeeping, and the working detail of features not
yet built — lives in `CLAUDE.local.md`, which is gitignored and never
committed. A session working in this repo reads both; a stranger reading
the repo needs only this one.

The split is a scope decision, not concealment. Nothing that moved was
secret, and every earlier revision of this file is still in the public
history where it has always been. What moved was the half that means
nothing to a reader outside the project and that could not stay legible
without dragging internal identifiers into a world-readable document —
against the rule stated directly below, in the file that states it.

## Public conclusions, not private citations

**Comments here state the CONCLUSION and the REASONING. They never cite
the private artefact the reasoning came from.** Both halves matter: the
reasoning is the point and must survive, but a reader of this repo
cannot follow a pointer into a document they will never see.

Concretely, none of these belong in any file this repository publishes —
code, comments, tests, fixtures, markdown, workflows or scripts:

- **Document section numbers and internal document versions** — a
  section sign followed by a number, or a version string naming an
  internal document.
- **Internal task, delivery-phase and ruling identifiers**, and any path
  into the private planning tree.
- **The control plane's storage key scheme** — its uppercase
  prefix-and-separator item keys and their attribute names — and the
  names of internal server-side packages.

The examples above are deliberately described rather than quoted: a rule
that forbids writing an identifier should not have to write one to say
so, and this file is as world-readable as the code beside it.

Rewrite rather than delete: "the server's transition table", "the
publish step writes the edge routing entry first", "the site's expiry is
written earlier still" all carry the full argument with nothing to
follow. A reason that cannot be stated without naming a private document
is a reason that needed rephrasing, not a citation that needed adding.

This is a boundary rule, not a threat model. No individual citation
leaks a secret; the point is that "does THIS one matter?" is a question
nobody should have to answer under time pressure, and git history is
permanent — a later commit cannot unpublish it.

**It is enforced mechanically, not only stated here.** `internal/guard`
reads `scripts/citation-patterns.txt` on every run and scans **every
file this repository publishes**: everything git tracks, plus every
untracked file no ignore rule covers. Markdown, the Makefile, workflow
YAML and shell scripts are all in scope, because every one of them is
exactly as world-readable as a `.go` file. Ignored files are out of
scope — git will not publish them, which is the same property that makes
`CLAUDE.local.md` safe to write in.

**The guard reads file CONTENTS, never file NAMES.** A branch name, a
commit message, a tag, a pull-request title and a release note are all
published surfaces that no pattern here can see. Those are checked by
hand at the moment of publishing.

## What this binary is — and what it is today

The finished product is one binary with two faces:

- `curious deploy [dir]` — pack an Astro project, upload it, stream the
  build, print the live URL.
- `curious mcp` — the same flow over stdio MCP, for agents. **Local
  stdio is a deliberate design choice rather than a stepping stone**: a
  remote MCP server cannot read the user's disk, and this tool's whole
  job is to pack the directory you are standing in.
- Distribution: a `curiouspub` npm wrapper whose postinstall fetches the
  release-built binary. The binary is named `curious` either way.

**What exists in the tree right now is smaller than that, and this
section says so on purpose.** Today `curious` dispatches `version` and a
`deploy` that exits non-zero as an unimplemented stub; `curious mcp` does
not exist yet; `internal/` holds packages scaffolded empty-but-real for
the changes that will fill them. A repository describing unbuilt features
in the present tense has told its reader something false, and this one is
read by strangers deciding whether to trust it.

## Layout

- `cmd/curious/` — entrypoint and subcommand dispatch only, no logic.
- `internal/` — `ui`, `config`, `api`, `preflight`, `pack`, `flow`,
  `guard`.
- `pkg/wire/` — **the public wire contract**, and the reason this
  repository is a Go module anyone can import.

### The wire contract is frozen in the only direction that matters

`pkg/wire` defines the `/v1` protocol **once**, here, in the public repo.
The server imports this module rather than the other way round, so the
contract is published before it is served.

Within v1 it is **additive only**. A new field or a new error code may
appear; renaming or removing a released field may not, and neither may
changing what one means. A released client is entitled to keep working,
and the module is public precisely so that promise is checkable rather
than merely stated. Golden tests are required for changes here.

Two consequences worth stating plainly, because both are easy to violate
with good intentions:

- **Don't invent endpoints or fields.** If a flow seems to need one, the
  contract is what needs changing, in a change that says so.
- **Don't reshape a wire type into a "nicer" internal one.** A second
  shape of the contract inside `internal/` is how the two drift, and the
  drift is invisible until a release breaks somebody.

## Dependency policy

Stdlib first. A third-party dependency is introduced only by a change
that names it and says why the stdlib answer was rejected.

**Never** an AWS SDK, a telemetry or analytics client, or an
auto-updater. This is enforced mechanically by `internal/guard`, not only
stated here.

## Build and CI

`make ci` is the single entry point, and CI runs it and nothing else, so
what passes locally and what passes in CI cannot diverge.

- `make fmt` `make vet` `make test` `make build` — `ci` runs them in that
  order, formatting first, so a formatting failure is not discovered
  after a five-minute suite.
- Builds use `-trimpath` and `CGO_ENABLED=0`, matching what the release
  will ship, so a release is not the first time those flags are
  exercised.
- **The suite runs with `-count=1`, and that is load-bearing rather than
  cautious.** Go's test cache keys on a package's declared inputs; the
  guards deliberately read things that are not inputs to their own
  package — the whole source tree, the module's dependency graph, a
  manifest file. A cached PASS therefore stays valid while the tree
  underneath it starts violating the rule. That is measured, not
  theorised: a banned import added elsewhere went undetected on a cached
  run and failed instantly without the cache.
- CI runs on **ubuntu, macos and windows**. Path handling, file
  permissions and the user config directory differ on all three, and this
  is a tool whose job is walking someone's project directory.
- `.gitattributes` normalises the checkout to LF. Without it, Windows
  checks out CRLF, `gofmt -l` reports every file in the repository as
  unformatted, and `ci` dies at its first step on files nobody touched.
- The Windows runner image ships no GNU Make, so the workflow installs
  one before running anything.
- Workflow actions are pinned to full commit SHAs with the human version
  in a trailing comment. Tags move; whoever can move one runs code in
  your job.
- **No secret is referenced by the CI workflow at all**, which is what
  lets the suite pass on a pull request from a stranger's fork.

## The guards

Four rules the repo states about itself are tests, so a violation fails
when it is introduced rather than at review. They live in
`internal/guard`.

1. **No AWS SDK, no telemetry dependency** — checked against the module's
   real dependency graph, both the package graph including test imports
   and the module requirement list. A requirement nothing imports yet is
   already the commitment.
2. **No compiled-in hostname**, beyond at most two named URL constants —
   the API base and the site base domain. A bare URL literal anywhere
   fails regardless of count: a URL a reader cannot find by grepping for
   one declaration is the shape a phone-home takes. The ceiling is stated
   inside the guard, because **a guard loosened by the thing that trips
   it is not a guard**.
3. **No private citation in any published file** — the rule at the top of
   this document, with `scripts/citation-patterns.txt` as its single
   source of patterns. The guard reads that file on every run and keeps
   no copy, so deleting a line measurably changes what the guard can see,
   which is how anyone can check it is really being read.
4. **No unexported struct field can reach a `Secret`.** Every other guard
   here enforces a rule the code could follow by accident; this one
   enforces a rule the language gives no way to express. `fmt` cannot
   call a method on a value it reached by reflecting an unexported field,
   so a secret held below one prints in full — and making the type opaque
   does not help, because the same reflection reaches the inner field. It
   is a **type** question, not a spelling one, so it asks about
   transitive containment rather than matching how a field was written.

Each guard fails loudly if it scanned nothing, so none of them can pass
by looking at an empty set.

## Hard don'ts

- **No telemetry, analytics, or phone-home of any kind. Ever.** This
  repo's existence is a trust argument; one tracker destroys it.
- **No AWS SDK.** The client speaks the public HTTP API and nothing else.
- **No server-side logic.** If a validation matters for security it
  belongs on the server; the checks here are UX, and every one of them is
  re-validated server-side.
- **Never print a token, a presigned URL, or full config** to the
  terminal or to MCP output. Secrets are held in a type that renders a
  placeholder through every formatting path, including JSON.

## Limits, and where they are enforced

A project is refused locally when it exceeds **3,000 files**, **5 MB for
any single file**, or **30 MB in total**. Packing always excludes
`node_modules/`, `dist/`, `.git/`, `.astro/` and `.env*`, and otherwise
respects `.gitignore`.

These are stated here because they are useful to know before you try. The
client checks them so you get a fast, local, specific answer instead of a
failed upload — but **the client is not the boundary.** Every one of them
is re-validated server-side, and that copy is the one that counts.

## Releases

GoReleaser: cross-platform binaries, checksums, GitHub Releases, a
Homebrew tap, and an npm wrapper publish as a pipeline step. Versions are
semver tags; a plain `go build` reports `dev` rather than guessing.

**Error messages are part of the product — write them for a clumsy
first-timer.** Every hard stop names an action the reader can take.
