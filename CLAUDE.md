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
- **The deployment estate** — resource identifiers, account numbers,
  internal endpoints, the name of the infrastructure provider serving the
  API, and the names of its individual services. What runs behind `/v1`
  is not a fact this repository has any reason to carry, and the client
  could not act on it if it did.

  **Runtime URLs are exempt, because they are physics rather than
  authorship.** The client is handed upload targets by the server and
  must send to whatever it receives; a hostname passing through a
  variable discloses nothing anyone chose to write. That exemption needs
  no mechanism, since such a URL never appears in a source file — and if
  a test fixture ever seems to need one, that is a signal the fixture is
  baking in an assumption this client is specifically designed not to
  have.

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

**Never** an infrastructure-provider SDK, a telemetry or analytics
client, or an auto-updater. This is enforced mechanically by
`internal/guard`, not only stated here.

The provider rule is written as a **class**, and the guard bans a long
list of providers rather than one. That is not thoroughness for its own
sake: this binary speaks exactly one protocol — this project's public
HTTP API — so it has no business holding any provider's SDK, and a guard
naming a single vendor would let every other vendor's SDK straight
through.

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
- **A SKIPPED ROW CANNOT PASS QUIETLY.** `go test` prints nothing for a
  skip without `-v`, so a green tick over a row that stopped running is
  indistinguishable from one over a row that passed — and this suite has
  rows that skip on a missing program, a filesystem feature an account
  cannot use, or a permission the runner does not hold, each of which is
  honest on one platform and a broken environment on another. `make test`
  therefore runs through `internal/skipcheck`, which prints every skip
  with the reason its author wrote, checks it against
  `scripts/expected-skips.txt`, and **fails the run on a skip nobody
  declared**. It passes its arguments through and returns the same exit
  code; a declared skip that did not happen is not a failure, because
  every one of these is conditional on the machine.

  The manifest is a fourth rule file read on every run with no copy
  kept — deleting a line measurably changes what the suite allows. One
  skip is deliberately absent from it: the row comparing this project's
  ignore-rule matcher against the real version-control program skips when
  that program is not installed, and the machine running these tests
  obtained the source with it, so that skip can only mean something is
  wrong.
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

1. **No provider SDK, no telemetry dependency, no auto-updater** —
   checked against the module's real dependency graph, both the package
   graph including test imports and the module requirement list. A
   requirement nothing imports yet is already the commitment. The
   fragments live in `scripts/banned-dependencies.txt` and the guard
   reads that file on every run, keeping no copy.
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

**Three of the four manifests are RULE FILES, and they get exactly one
narrow carve-out.** `scripts/citation-patterns.txt`,
`scripts/banned-dependencies.txt` and `scripts/vendor-terms.txt` have to
spell the things they forbid — you cannot match a module path without
writing one down — so their **data** lines are exempt **from the vendor
check only, never from any other pattern**. That narrowness is the whole
of it: the dependency denylist genuinely must name providers, and nothing
else about a data line deserves a waiver, so a private identifier written
on one still reds. Their **comment** lines are scanned like any other
prose, which is why nothing in any of them quotes an example — a rule
file that quoted its own targets would be a leak shipping inside the file
that hunts leaks.

**A `go.sum` CHECKSUM COLUMN is exempt from the vendor check. Nothing
else is** — not the rest of that line, and not `go.mod`. The line is
GENERATED versus AUTHORED, not "manifest" and not even "file": a
`go.sum` line carries a machine-chosen hash **and** a human-chosen module
path, side by side, and only the first of them is an accident.

That distinction was learned the expensive way. The first version of this
rule excused the whole file on the grounds that nobody chooses the bytes
of a hash — true of the hash, false of the path beside it. `go.sum`
retains entries for modules no longer in the build graph until someone
runs `go mod tidy`, so a provider SDK named in a stale entry became
invisible here; and the dependency check could not see it either, since
`go list -m all` omits a module nothing imports. Two rules, one blind by
construction and one blinded by a carve-out drawn wider than its own
argument.

That distinction is load-bearing rather than tidy, because the vendor
check reads subwords inside identifiers and base64 produces capitalised
fragments freely. A module hash can therefore spell a banned term. Over
200,000 random hashes against the real term list, **0.72% of lines trip**
— four lines is a 2.8% chance, twenty is 13.5%, fifty is nearly a third.
The damage is not the red but what it would force: a dependency bump
nobody chose the bytes of breaking the build on a file no author can
edit, whose only quick fix is deleting a term from the vendor list. That
is the rule weakened by the thing that trips it, which is the same
failure the carve-out above exists to prevent.

The exemption is by FILENAME, not by content shape. A heuristic like
"looks like base64" would also excuse an authored line that happened to
look generated, and nothing from outside could tell which had happened.
It is also scoped to the module's own manifest: a file called `go.sum`
somewhere else in the tree is not exempt.

**The vendor check TOKENISES rather than pattern-matches**, and that is
not an optimisation. A word-boundary expression sees no boundary inside
an identifier, so every camelCase and snake_case spelling walked past the
first version of this rule; a substring match instead reds on ordinary
English, because a common word for defects contains one of the terms. So
the guard splits each alphanumeric run on camelCase and acronym
boundaries and matches whole subwords, plus contiguous joins of them for
names the convention itself splits.

## Hard don'ts

- **No telemetry, analytics, or phone-home of any kind. Ever.** This
  repo's existence is a trust argument; one tracker destroys it.
- **No infrastructure-provider SDK.** The client speaks this project's
  public HTTP API and nothing else — never whatever runs behind it.
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
