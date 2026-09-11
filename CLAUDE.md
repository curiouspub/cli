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

**The guard reads file CONTENTS, never file NAMES**, and that is a
statement about that guard rather than about this rule's coverage. A
branch name, a commit message, a tag and its message, and a pull
request's title and body are published exactly as loudly as a file and
are not files, so they are read by a **second** check — `make
surface-check`, which runs in CI over the range being published and in an
optional pre-push hook before it leaves your machine. It reads the same
two rule files this one does, at both ends of the range, and it is
described where it lives.

That check exists because the hand-check it replaces failed twice here,
once by the same hand that had written the sentence describing the hole.
**A rule with no mechanism gets followed until the moment somebody is
busy**, and that is the moment a release is cut.

**What is still hand-checked, stated exactly, because implying coverage
is worse than having none:** a release note, which is authored after the
tag and is not a git object at all; and a message hand-edited in the
merge button at the moment of merging, which the push check on the
default branch sees only after publication. That is detection plus
repair, and calling it enforcement would be the claim this whole
mechanism exists because somebody made once already.

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
section says so on purpose.** Today `curious` dispatches `version`, a
`deploy` that runs the whole sequence and ends with the site's address,
and an `mcp` server that serves **four tools** — `login_start`,
`login_verify`, `deploy_site` and `deploy_status` — over the same
sequence the command runs. A repository describing unbuilt features in
the present tense has told its reader something false, and this one is
read by strangers deciding whether to trust it. **The reverse is no
smaller an error**: a line still saying the tools are unbuilt, the day
after they ship, is the same defect running backwards, so this paragraph
moves in the commit that moves the code.

**There is no `whoami`, and its absence is a decision rather than a
gap.** It would have to wrap a route the API does not serve, so every
call to it would answer an agent with a transport failure — and a model
that can see a tool keeps trying it. There is no endpoint that answers a
deploy's current state either, which is why `deploy_status` reads the
build log and reports `"not yet reported"` until that log says how the
deploy ended: **a live tail is not a state read**, and the honest shape
admits it rather than promoting the last phase to a status. The two
vocabularies share spellings, so that guess would look right until the
day it was not.

**A tool description may state a number only when the number is one the
wire contract holds.** The size and count limits are the server's own
and are read from `pkg/wire`; quota and expiry are server POLICY, which
moves without a release and which this binary is never told, so they are
described in words. A description stating a limit the server does not
enforce is worse than one stating none, because a caller acts on it —
and a guard parses the rendered descriptions and requires every figure in
them to be one of the contract's.

`deploy` resolves the directory, reads any stored login, walks the
project once, runs the pre-flight checks and the local limits over that
one walk, checks capacity and logs in when there is no usable token,
packs the archive, asks the server for somewhere to send it, uploads it,
asks the server to build, renders the build log until the build
finishes, publishes the built deploy, and prints the address it answers
at — **with the archive removed on the way out of every path.** The
ordering is the product rather than an implementation detail: everything
free and local runs first, so **a project that cannot deploy makes zero
network calls**, and nobody is walked through email verification before
being told there is no `package.json`.

Four properties of the network half are worth stating here, because each
is easy to undo by accident.

**Authentication is per CALL, not per client**: the create, the start and
the event stream send the bearer token, the four unauthenticated
endpoints cannot, and the upload cannot either — the presigned link IS
the credential, and adding a second one sends it to an origin that never
asked.

**The upload's failure copy never quotes the link**: every transport
failure net/http produces carries the whole signed URL, so the single
error constructor and the redacted host are what keep a credential out of
a message.

**Neither long-running step takes a total deadline, and that is one
ruling applied twice rather than a habit.** The upload waits for the next
BYTE and the stream waits for the next byte too; nothing bounds either as
a whole. A deadline cannot express "is this making progress", and the
same 30 seconds that is generous for a small request and a small answer
kills a large upload on an ordinary uplink and any build quieter than
half a minute. A constant carries its number and not the reason the
number was chosen, so each of these windows is picked where it is used
and says what it bounds there.

**The last line does not claim the site is reachable.** An address
starts answering a little after the deploy that owns it is published —
observed once at about half a minute, which is ONE measurement and not
an upper bound, so the copy says "up to about a minute" rather than
naming a figure nobody measured twice. Nothing polls the address and
nothing sleeps: a delay is a guess, a guess long enough to be safe costs
more than the wait it hides, and one location's answer is not the claim
being made. Machinery that genuinely waits belongs in the control plane,
where every surface inherits it, rather than in this one command.

**Everything the build prints is escaped before it reaches the
terminal.** The build log is arbitrary program output — the package
manager and the site builder pass through whatever a project prints — and
a terminal obeys some of those bytes. The service escapes the same set at
its end; this client escapes it again, because a local safety property
must not rest on a remote guarantee it cannot verify, watch regress, or
version-check.

The split between the server and its tools is deliberate rather than a
staging accident: a server with no tools is testable against the protocol
alone, and every protocol defect found there is one not found while also
debugging a deploy.

## Layout

- `cmd/curious/` — entrypoint and subcommand dispatch only, no logic.
- `internal/` — `ui`, `config`, `api`, `preflight`, `pack`, `check`,
  `flow`, `mcp`, `units`, `guard`.
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

**`make ci` is the whole TEST GATE, and every check that can fail a change
runs inside it**, so what passes locally and what passes in CI cannot
diverge. That is the invariant; "CI runs one workflow" was the shape it
happened to have, and the shape changed the day the release pipeline
landed.

Three workflows run today, and only the first can fail an ordinary change:

| workflow | trigger | what it does |
|---|---|---|
| `ci.yml` | every push, every pull request, every merge-queue entry | `make ci` on the three-OS matrix — **the gate** — and `make surface-check` on one leg |
| `snapshot.yml` | pull requests touching the release surface | `make snapshot`: the full build matrix, archives and checksums, no upload and no tokens |
| `release.yml` | a version tag only | the real publish, behind an environment with a required reviewer |

The second and third are the release pipeline proving and performing
itself; both run a `make` target, so neither is a command that exists only
in a workflow file. **The rule that matters is not the count of workflows
but that no check lives outside a `make` target** — a step written only in
YAML is a step nobody can run before pushing.

- `make fmt` `make vet` `make test` `make build` — `ci` runs them in that
  order, formatting first, so a formatting failure is not discovered
  after a five-minute suite.
- **`make surface-check` is the one check `ci` cannot carry, and the
  reason is the shape of its subject rather than its cost.** There is no
  push range in a working copy: a checkout is one state, and that check is
  about the difference between two. What `ci` does carry is the checker's
  own suite, so the instrument is tested by every run even though the
  measurement needs a range to point at. It also runs FIRST in
  `release.yml`, with every later job depending on it, because a tag push
  does not run `ci.yml` at all.

  **THE OPERATOR ACTION IS DONE — 2026-09-09 — and what it turned on is
  recorded here because nothing inside the repository can read it back.**
  `ci.yml` is wired to the merge-queue event, which is where the commit a
  squash merge composes — out of a pull request's title and body — can be
  read before it lands. That event only ever fires if somebody with
  repository settings enables the merge queue on the default branch
  **and** marks this check required; until both were done the trigger was
  a line that never ran.

  `main` now carries a ruleset: pull requests required, force-push and
  deletion blocked, branches up to date before merging, merge queue on.
  A direct push is refused with *"Changes must be made through the merge
  queue"* — verified by attempting one.

  **The seven required checks, spelled exactly as GitHub names them:**

  | required check |
  |---|
  | `ci (ubuntu-latest)` |
  | `ci (macos-latest)` |
  | `ci (windows-latest)` |
  | `the wrapper package, on the Node floor and on current (22)` |
  | `the wrapper package, on the Node floor and on current (current)` |
  | `the published surfaces that are not files` |
  | `build every artefact and publish none of them` |

  **THE `(22)` ENTRY IS A VERSION NUMBER IN A SETTING NOBODY HERE CAN
  EDIT, and that is the sharp edge.** A required check is matched by
  NAME. The Node floor moves — it is in `npm/package.json` and it will
  rise — and the day the matrix says `["24", "current"]` the check called
  `… (22)` stops being produced. GitHub does not fail a merge for a
  required check that never arrives from a job that no longer exists; it
  simply has one fewer gate, and `main` is un-gated on the floor leg
  until somebody edits the ruleset to match. The same is true of any job
  RENAME: the name in this table, the name in the workflow and the name
  in the ruleset are three copies of one string, and only two of them
  live in this repository.

  **AND A REQUIRED CHECK HAS TO BE PRODUCED WHERE IT IS WAITED FOR.**
  A workflow not wired to `merge_group` never produces its check inside
  the queue, so the entry waits for it for ever: the queue STALLS rather
  than failing, which is the quieter of the two and the one nobody gets
  told about. This repository's queue did exactly that on its first run —
  the snapshot workflow was wired to `pull_request` and `push`, and its
  job is required.

  So a guard compares the first two — `internal/guard` derives the check
  names from the workflows, requires them to be exactly this table, and
  requires each one's workflow to fire on the queue's event.
  A rename reds in CI, loudly, before it goes quiet in the ruleset. The
  third copy is still a setting somebody has to change by hand, and the
  guard's message says so.

  **This section is the pointer the surface check's task called "the
  runbook".** There is no runbook; this is the list this repository has,
  and a reference to a document that does not exist is worse than no
  reference at all.
- **`make hooks` installs the same check as a pre-push hook, and it is opt
  in.** A hook lives in a directory git does not clone, is skipped by
  `--no-verify` and is absent on CI, so it is a convenience and never the
  gate — anything that has to hold holds in the workflow. What it buys is
  the failure arriving in a couple of seconds on the machine that wrote
  the message. What lands in the hooks directory is a shim that runs the
  tracked script, so it cannot go stale when that script changes; a copy
  would, and the copy nobody re-installed is the one running when it
  matters.
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
  therefore runs through `tools/skipcheck`, which prints every skip
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

The rules the repo states about itself are tests, so a violation fails
when it is introduced rather than at review. They live in
`internal/guard`, except the last, which lives beside the value it checks
for the reason given under it.

Counted by the list rather than by a numeral here. A count in prose
beside a list that grows is a fact with an expiry date, and this
sentence has already carried a stale one.

1. **No provider SDK, no telemetry dependency, no auto-updater** —
   checked against the module's real dependency graph, both the package
   graph including test imports and the module requirement list. A
   requirement nothing imports yet is already the commitment. The
   fragments live in `scripts/banned-dependencies.txt` and the guard
   reads that file on every run, keeping no copy.
2. **No compiled-in hostname**, beyond a small pinned set of named URL
   constants whose VALUES are approved one by one. A bare URL literal
   anywhere fails regardless of count: a URL a reader cannot find by
   grepping for one declaration is the shape a phone-home takes. The
   ceiling is stated inside the guard, because **a guard loosened by the
   thing that trips it is not a guard**. The site base domain is not one
   of them and takes none of that budget: it is a bare host with no
   scheme, so it is not a URL literal at all, and the scheme is added
   where the address is composed — through `net/url`, so no source file
   carries the scheme fragment either.
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
5. **The exported check ids and the declared universe are one set** —
   asserted in both directions, and the two sides deliberately come from
   different mechanisms: the constants are read out of the source by a
   type checker, the universe is the compiled program's own answer. It
   replaced a row that compared two hand-written maps, which agreed with
   each other by construction and never looked at the universe at all.
   Like guard 4 it asks a **type** question — an exported constant of
   bare string type — rather than matching a name, so an id spelled
   without the usual prefix is still seen. The consequence is worth
   knowing before it surprises anyone: an exported string constant in
   that package which is not a check id must be given a defined type,
   which is what every other family of constants there already has.
6. **The platforms the skip manifest accepts are the legs CI runs** —
   again both directions, since a platform accepted but never run makes
   a rule that can only look like coverage, and a leg that runs but is
   rejected makes a legitimate skip undeclarable. It lives in
   `tools/skipcheck` rather than with the others because there it reads
   the real variable; from outside it would have to scrape a slice
   literal out of the syntax tree, which is a second transcription of
   the value and the exact defect guard 5 was created to remove.

Each guard fails loudly if it scanned nothing, so none of them can pass
by looking at an empty set.

**The manifests that are RULE FILES get exactly one narrow carve-out,
and the skip manifest is not one of them.** `scripts/citation-patterns.txt`,
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
any single file**, or **30 MB in total**, and once more after packing if
the archive itself comes out over 30 MB. Packing always excludes
`node_modules/`, `.git/`, `.DS_Store`, `Thumbs.db`, a root `dist/` or
`.astro/`, and anything starting `.env`; otherwise it respects
`.gitignore`.

**MB here means 1,000,000 bytes and not 1,048,576.** Client and server
must not each pick their own reading of the same number: the 5%
difference between them surfaces nowhere except at the boundary, as a
refusal whose numbers nobody can make agree.

The fourth limit is the one you can hit having passed the other three,
and it is not a contradiction. Compression helps the file data; the
archive adds bytes of its own for every entry it holds, so a great many
small files that do not compress can weigh more packed than they do on
disk. The message says so, and names the files worth removing.

These are stated here because they are useful to know before you try. The
client checks them so you get a fast, local, specific answer instead of a
failed upload — but **the client is not the boundary.** Every one of them
is re-validated server-side, and that copy is the one that counts.

The list above is prose for a reader; the program renders the excluded
set from the walk's own rules, so a message about it cannot drift from
what was actually excluded. This paragraph can, which is why the code
does not read it.

## Releases

GoReleaser: cross-platform binaries, checksums, GitHub Releases, a
Homebrew tap, and an npm wrapper publish as a pipeline step. Versions are
semver tags; a plain `go build` reports `dev` rather than guessing.

**Error messages are part of the product — write them for a clumsy
first-timer.** Every hard stop names an action the reader can take.
