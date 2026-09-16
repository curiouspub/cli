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
- **`curious mcp`** — the stdio MCP server, so an agent deploys the same
  way a person does. It serves four tools: `login_start` and
  `login_verify` get a login onto the machine, `deploy_site` runs the
  whole deploy and answers with the address, and `deploy_status` reports
  what a deploy's build log said. They drive the same sequence
  `curious deploy` drives rather than a copy of it, and a client that
  asks to watch a call is sent progress as the build goes. Written
  against the standard library alone — no SDK, no new dependency.
- **`curious deploy`** — it checks your project, logs you in if it has
  to, packs the archive, uploads it, starts the build, streams the build
  log to your terminal until the build finishes, publishes the result
  and prints the `*.curiously.dev` address it answers at. Everything it
  can answer without the network it answers first, so a project that
  cannot deploy never sends a byte. The build log and the address go to
  stdout and everything the tool says about them goes to stderr, so
  redirecting stdout collects the log and the address and nothing else —
  and every line of the log is escaped on the way out, because a build
  log is arbitrary program output and your terminal obeys some of it.
  The last line does not tell you the site is live: an address takes a
  little while to start answering everywhere, so the tool says the
  deploy was published and tells you what to do if you get there first.

The trial opens in small daily batches. Get the launch note:
[hello -a- curious.pub]

## What this is

`curious` packs an Astro project, sends it to curious.pub, and hands you
back a public address in about the time a build takes — no dashboard, no
project setup beyond an email address. What comes back is a **temporary
preview**: every site curious.pub builds for you expires after a period
of inactivity, which is what makes trying it free of any commitment.

## Try it

Once the trial is open, this is the whole flow — see "Status:
pre-release" above and "Installing it" below for what exists today:

```
npx curiouspub deploy
```

The first run asks for your email address, sends a 6-digit code, and
asks you to type it back — that's the whole login, and it is free to
repeat if the code expires or you mistype it. Once you're in,
`curious` checks your project locally first (nothing leaves your
machine if a check fails), packs it, uploads the archive, and streams
the build to your terminal. When the build finishes it prints the
address your site answers at.

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

On macOS, once a release exists:

```
brew install curiouspub/tap/curious
```

The tap ships a cask rather than a formula — this project ships
prebuilt binaries, and a cask is the supported shape for that — which
means it installs on macOS only. Everywhere else, take the npm wrapper
or a binary from Releases.

**A binary from Releases, verified before you run it.** Download the
archive for your platform and `checksums.txt` from the same release,
then check the archive against the checksum **before** extracting it:

```
sha256sum -c checksums.txt --ignore-missing
```

The checksum step is the point of shipping checksums at all; a README
that does not show the command means nobody runs it. `checksums.txt`
is itself signed, keylessly, by the workflow that built it — a detail
for an auditor with the verifying tool, and not something this command
checks for you.

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

## What gets uploaded

`curious` packs the project directory you point it at, and nothing more.
[`.gitignore`](https://git-scm.com/docs/gitignore) is respected — if git
would not track it, `curious` will not send it — and on top of your own
rules a small, fixed set is always left out. You deserve to know what
leaves your machine before it does.

| name | matches |
|---|---|
| `node_modules` | any depth |
| `.git` | any depth |
| `.DS_Store` | any depth |
| `Thumbs.db` | any depth |
| `dist` | project root only |
| `.astro` | project root only |
| `.env` | any name beginning with it, any depth |

Matched by **name**, never by kind: a linked worktree and a submodule
both spell `.git` as a *file* holding a pointer rather than a directory,
so this table carries no trailing slashes — a slash would claim a
directory-only rule that does not exist. A `dist` or `.astro` directory
nested somewhere other than the project root is **not** forcibly
excluded by this table; your ordinary ignore rules still decide its
fate.

Four numbers bound what `curious` will pack locally, read from the same
contract the server checks again on its own side — the client's answer
is a fast, local, specific one; it is never the boundary:

- `MaxSourceFiles` — 3,000 files at most.
- `MaxSourceFileBytes` — 5 MB for any single file.
- `MaxSourceTotalBytes` — 30 MB of source in total.
- `MaxPackedBytes` — 30 MB once packed.

Passing the first three does not guarantee passing the fourth. An
archive adds bytes of its own for every entry it holds, so a great many
small, incompressible files can weigh more once packed than they do on
disk — `curious` names the files worth removing when that happens.

## Where the login lives

The login is a token, held in a small JSON file `curious` reads and
writes for you — never edited by hand, and never printed anywhere,
terminal or agent. Its location follows the first of these that
applies:

1. `CURIOUS_CONFIG`, if set — the exact file, used exactly as given.
2. `$XDG_CONFIG_HOME/curious/config.json`, if `XDG_CONFIG_HOME` is an
   absolute path.
3. Otherwise, wherever your platform keeps a small tool's settings (for
   example `~/.config/curious/config.json` on Linux and macOS).

On macOS and Linux the file is written `0600` — readable and writable
by you and nobody else — and `curious` checks that on every run and
tells you if it has changed. **On Windows this guarantee does not
hold.** Windows expresses "only the owner may read this" through an
access-control list rather than the mode bits this program can set, so
on Windows the file simply inherits whatever permissions its directory
already carries. That is a documented gap, not an implied protection.

`curious` has no logout command. Deleting the file logs you out; the
next deploy asks you to log in again.

## Principles

**Zero telemetry.** No analytics, no auto-update, no phone-home, no
exceptions. Every run's network traffic is exactly two things: it
authenticates to the API you tell it to talk to, and, once per deploy,
it uploads the packed archive to a single address that API hands back
for that deploy alone. Nothing else is contacted at runtime — installing
the tool is a separate step, covered under Installing it above.

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

## Troubleshooting

Every hard stop `curious` prints ends with a line like `Failure ID:
upload-link-expired` — paste that id into a search box, or find it
below. Entries are grouped by id; beneath each is the stage it can meet
you at (what you were doing when it did) and, where the message is
fixed text rather than something assembled from the situation at run
time, the message itself — quoted straight from this repository's own
failure catalog (`catalog.json`) so it cannot silently drift from what
the program actually prints.

<!-- failure-headlines -->

### answer-not-understood

**questions.** A person is being asked a question that curious cannot put to them or cannot read the answer to.

> Didn't catch that.

### api-address-unusable

**this machine.** A person is getting this machine ready before anything is sent: the project directory, where the login is kept, the API address or the temporary directory.

> curious can't use that API address.

### archive-unreadable

**uploads.** A person is waiting while curious sends the packed project to the upload address.

_The message here is assembled at run time, so there is no fixed sentence to quote — see "A note on composed messages," below._

It names the address curious was sending to, reduced to its host, and
quotes what your machine said when the packed archive could not be opened
to send it.

### astro-dep-absent

**pre-flight.** A person is having the project checked before anything leaves the machine.

> This doesn't look like an Astro project.

### astro-dep-invalid-json

**pre-flight.** A person is having the project checked before anything leaves the machine.

> package.json isn't valid JSON.

### astro-dep-missing

**pre-flight.** A person is having the project checked before anything leaves the machine.

> This doesn't look like an Astro project.

### astro-dep-not-object

**pre-flight.** A person is having the project checked before anything leaves the machine.

> package.json isn't a JSON object.

### astro-dep-unreadable

**pre-flight.** A person is having the project checked before anything leaves the machine.

> curious couldn't read package.json.

### build-failed

**building.** A person is waiting while the server builds the project and decides whether to take the result.

> The build failed.

### build-log-lost

**build log.** A person is watching the build log stream in while the build runs.

> curious lost the build log and could not pick it up again.

### build-output-refused

**building.** A person is waiting while the server builds the project and decides whether to take the result.

> The build finished, and the server would not take the result.

### capacity-check-failed

**deploys.** A person is starting a deploy: curious is asking whether there is room today and for somewhere to upload the project.

> curious couldn't ask whether there is room today.

### capacity-check-unreachable

**deploys.** A person is starting a deploy: curious is asking whether there is room today and for somewhere to upload the project.

> curious couldn't ask whether there is room today.

### client-request-rejected

**deploys.** A person is starting a deploy: curious is asking whether there is room today and for somewhere to upload the project.

> The server wouldn't accept that archive.

**logins.** A person is logging in, or curious is saving the login they just completed.

> curious sent something this server wouldn't accept.

### config-location-unusable

**this machine.** A person is getting this machine ready before anything is sent: the project directory, where the login is kept, the API address or the temporary directory.

> curious couldn't work out where to keep your login.

### daily-capacity-closed

**addresses.** A person is waiting for a finished deploy to be given its public address.

> curious.pub is full for today.

**deploys.** A person is starting a deploy: curious is asking whether there is room today and for somewhere to upload the project.

> curious.pub is full for today.

### deploy-not-completed-by-server

**addresses.** A person is waiting for a finished deploy to be given its public address.

> The server couldn't finish the deploy.

### deploy-unknown-to-server

**addresses.** A person is waiting for a finished deploy to be given its public address.

> The server doesn't know that deploy.

### fresh-login-refused

**deploys.** A person is starting a deploy: curious is asking whether there is room today and for somewhere to upload the project.

> Authentication failed.

### internal-fault

**this run.** A person could be anywhere in a run, and the fault is in curious itself.

> Something went wrong inside curious.

### limit-file-size

**pre-flight.** A person is having the project checked before anything leaves the machine.

_The message here is assembled at run time, so there is no fixed sentence to quote — see "A note on composed messages," below._

It names how many files are over the single-file limit and what that
limit is, then lists them largest first with their sizes — ten at most,
and a count of however many more there were.

### limit-files

**pre-flight.** A person is having the project checked before anything leaves the machine.

_The message here is assembled at run time, so there is no fixed sentence to quote — see "A note on composed messages," below._

It names how many files the project holds and how many one deploy may
carry, then the handful of directories holding the most of them — so you
have somewhere to point an ignore rule rather than a list to read.

### limit-packed

**pre-flight.** A person is having the project checked before anything leaves the machine.

_The message here is assembled at run time, so there is no fixed sentence to quote — see "A note on composed messages," below._

It names what the archive weighs, what the limit is, and what your files
weigh unpacked — the gap between the last two being the archive's own
overhead — and then the files that compressed least, with their sizes.

### limit-total

**pre-flight.** A person is having the project checked before anything leaves the machine.

_The message here is assembled at run time, so there is no fixed sentence to quote — see "A note on composed messages," below._

It names how much source the project comes to and how much one deploy may
carry, then the largest files with their sizes — ten at most, and a count
of however many more there were.

### lockfile-missing

**pre-flight.** A person is having the project checked before anything leaves the machine.

> No lockfile found.

### lockfile-unsupported

**pre-flight.** A person is having the project checked before anything leaves the machine.

> No lockfile curious can install from.

### lockfile-workspace

**pre-flight.** A person is having the project checked before anything leaves the machine.

> This looks like a package inside a workspace.

### login-endpoint-missing

**logins.** A person is logging in, or curious is saving the login they just completed.

> That server does not answer the login endpoint.

### login-not-saved

**logins.** A person is logging in, or curious is saving the login they just completed.

> Logged in, but the login could not be saved.

### login-refused

**logins.** A person is logging in, or curious is saving the login they just completed.

> That login was refused.

### needs-a-terminal

**questions.** A person is being asked a question that curious cannot put to them or cannot read the answer to.

> curious needs a terminal for that.

### path-charset

**pre-flight.** A person is having the project checked before anything leaves the machine.

_The message here is assembled at run time, so there is no fixed sentence to quote — see "A note on composed messages," below._

It names the one path that cannot be published and what is wrong with it:
a space, a character shown with its code point, a mark that attaches to
the letter before it, or a length in bytes against the limit it passed.
One message per path, because the reason differs per path.

### project-dir-missing

**this machine.** A person is getting this machine ready before anything is sent: the project directory, where the login is kept, the API address or the temporary directory.

_The message here is assembled at run time, so there is no fixed sentence to quote — see "A note on composed messages," below._

It names the full path there is nothing at, resolved from whatever you
typed — so you can see which directory curious actually went looking in,
which is usually the whole of the misunderstanding.

### project-dir-unknown

**this machine.** A person is getting this machine ready before anything is sent: the project directory, where the login is kept, the API address or the temporary directory.

> curious couldn't work out which directory you mean.

### project-dir-unreadable

**this machine.** A person is getting this machine ready before anything is sent: the project directory, where the login is kept, the API address or the temporary directory.

_The message here is assembled at run time, so there is no fixed sentence to quote — see "A note on composed messages," below._

It names the full path curious could not read, and quotes what your
machine said about it rather than paraphrasing it.

### project-not-ready

**pre-flight.** A person is having the project checked before anything leaves the machine.

> curious can't deploy this project yet.

### project-path-not-a-directory

**this machine.** A person is getting this machine ready before anything is sent: the project directory, where the login is kept, the API address or the temporary directory.

_The message here is assembled at run time, so there is no fixed sentence to quote — see "A note on composed messages," below._

It names the full path, and says it is a file rather than a directory.
curious deploys the directory holding package.json, not a single file
inside it.

### project-unreadable

**this machine.** A person is getting this machine ready before anything is sent: the project directory, where the login is kept, the API address or the temporary directory.

> curious couldn't read the whole project.

### publish-not-confirmed

**building.** A person is waiting while the server builds the project and decides whether to take the result.

> The build could not be confirmed in time.

### published-address-invalid

**addresses.** A person is waiting for a finished deploy to be given its public address.

> The server gave this deploy an address that cannot be one.

### rate-limited

**addresses.** A person is waiting for a finished deploy to be given its public address.

> The server is asking for a pause.

**deploys.** A person is starting a deploy: curious is asking whether there is room today and for somewhere to upload the project.

> Too many requests from here.

**logins.** A person is logging in, or curious is saving the login they just completed.

> Too many requests from here.

### server-answer-unrecognised

**addresses.** A person is waiting for a finished deploy to be given its public address.

> The server wouldn't give this deploy an address.

**build log.** A person is watching the build log stream in while the build runs.

> The server wouldn't send the build log.

**building.** A person is waiting while the server builds the project and decides whether to take the result.

> The server wouldn't start the build.

**deploys.** A person is starting a deploy: curious is asking whether there is room today and for somewhere to upload the project.

> curious couldn't start the deploy.

**logins.** A person is logging in, or curious is saving the login they just completed.

> curious couldn't finish logging you in.

### server-unanswered

**addresses.** A person is waiting for a finished deploy to be given its public address.

> curious didn't hear back after asking for the deploy's address.

**building.** A person is waiting while the server builds the project and decides whether to take the result.

> curious didn't hear back after asking the server to build.

**deploys.** A person is starting a deploy: curious is asking whether there is room today and for somewhere to upload the project.

> curious didn't hear back after asking for somewhere to upload.

### service-unavailable

**addresses.** A person is waiting for a finished deploy to be given its public address.

> curious.pub isn't giving out addresses right now.

**build log.** A person is watching the build log stream in while the build runs.

> curious.pub stopped sending the build log.

**building.** A person is waiting while the server builds the project and decides whether to take the result.

> curious.pub is not building right now.

**deploys.** A person is starting a deploy: curious is asking whether there is room today and for somewhere to upload the project.

> curious.pub is not taking deploys right now.

**logins.** A person is logging in, or curious is saving the login they just completed.

> curious.pub is not taking logins right now.

**this request.** A person asked curious.pub for something and the server closed the door without saying which step it was.

> curious.pub isn't taking this right now.

### temp-dir-unusable

**this machine.** A person is getting this machine ready before anything is sent: the project directory, where the login is kept, the API address or the temporary directory.

> curious couldn't make a place to write the archive.

### upload-address-unusable

**uploads.** A person is waiting while curious sends the packed project to the upload address.

_The message here is assembled at run time, so there is no fixed sentence to quote — see "A note on composed messages," below._

It names the host, and nothing else about the address: a link curious
could not even build a request from is still a link, and the part of it
that authorises the upload is not something to print.

### upload-answer-unrecognised

**uploads.** A person is waiting while curious sends the packed project to the upload address.

_The message here is assembled at run time, so there is no fixed sentence to quote — see "A note on composed messages," below._

It names the host and the status number that came back, which together
are the whole of what this client knows about an answer it cannot
explain.

### upload-connection-lost

**uploads.** A person is waiting while curious sends the packed project to the upload address.

_The message here is assembled at run time, so there is no fixed sentence to quote — see "A note on composed messages," below._

It names the host the archive had already reached. Bytes moved before the
connection went, which is what separates this from an address that never
answered at all.

### upload-host-unreachable

**uploads.** A person is waiting while curious sends the packed project to the upload address.

_The message here is assembled at run time, so there is no fixed sentence to quote — see "A note on composed messages," below._

It names the host nothing was sent to. Not one byte left this machine,
which is what distinguishes it from a transfer that stopped part way
through.

### upload-link-expired

**uploads.** A person is waiting while curious sends the packed project to the upload address.

_The message here is assembled at run time, so there is no fixed sentence to quote — see "A note on composed messages," below._

It names the host, and says the upload window had already closed when the
archive got there. Those windows are short on purpose, and a large
project on a slow connection can outlast one.

### upload-redirected

**uploads.** A person is waiting while curious sends the packed project to the upload address.

_The message here is assembled at run time, so there is no fixed sentence to quote — see "A note on composed messages," below._

It names the host and the status number it answered with. curious does
not follow a redirect while it is sending your project: the address it
was given is the only one the server put its name to.

### upload-refused-unexplained

**uploads.** A person is waiting while curious sends the packed project to the upload address.

_The message here is assembled at run time, so there is no fixed sentence to quote — see "A note on composed messages," below._

It names the host, and then says plainly that it cannot tell which of two
things happened, because this server does not yet announce when an upload
window closes. Running again is worth one try.

### upload-signature-mismatch

**uploads.** A person is waiting while curious sends the packed project to the upload address.

_The message here is assembled at run time, so there is no fixed sentence to quote — see "A note on composed messages," below._

It names the host, and says the window had not closed yet — so what was
refused was the archive itself rather than the timing. That is a fault in
curious rather than anything about your project, and running again will
not move it.

### upload-stalled

**uploads.** A person is waiting while curious sends the packed project to the upload address.

_The message here is assembled at run time, so there is no fixed sentence to quote — see "A note on composed messages," below._

It names the host and how long nothing moved. The connection was open the
whole time, which is a different situation from one that was never made,
and the message says so rather than blaming your network.

### waitlist-declined

**waitlist.** A person found today's capacity used up and is being offered the waitlist.

> Nothing has been deployed.

### waitlist-needs-terminal

**waitlist.** A person found today's capacity used up and is being offered the waitlist.

> Nothing has been deployed.

### waitlist-signup-failed

**waitlist.** A person found today's capacity used up and is being offered the waitlist.

> That didn't get you onto the list.

<!-- /failure-headlines -->

### A note on composed messages

Eighteen (id, stage) pairs build their message from the situation at run
time rather than writing one fixed sentence, and are deliberately not
templated above: a hand-typed guess at their shape would be wording
nothing checks, which is the exact fragility this whole section exists
to end. They are:

- **uploads** — `archive-unreadable` and the nine `upload-*` ids:
  `upload-address-unusable`, `upload-answer-unrecognised`,
  `upload-connection-lost`, `upload-host-unreachable`,
  `upload-link-expired`, `upload-redirected`,
  `upload-refused-unexplained`, `upload-signature-mismatch`,
  `upload-stalled`.
- **pre-flight** — `limit-file-size`, `limit-files`, `limit-packed`,
  `limit-total`, `path-charset`.
- **this machine** — `project-dir-missing`, `project-dir-unreadable`,
  `project-path-not-a-directory`.

## Exit codes

| code | meaning |
|---|---|
| 0 | success, or a run you cancelled yourself |
| 1 | `curious` stopped for the reason named in the message printed above it |
| 2 | `curious` was called wrong — an unknown flag, a bad argument, or no command at all |
| 3 | curious.pub is closed to this run right now (the kill switch, or a capacity cap) |
| 130 | the run was interrupted (Ctrl-C, or a signal from outside it) |

In a script, `0` means the run ended the way it meant to — which
includes a deploy you cancelled yourself, not only one that published.
Anything else names what stopped it.

## Contributing

Early days and a small team, so issues are welcome, PRs may wait, and
the protocol itself changes only through the platform's design process.
If you've found a security issue, mail
[abuse -a- curious.pub] instead of opening an
issue.

## License

[MIT](./LICENSE)

*Published curiously.*
