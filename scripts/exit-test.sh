#!/usr/bin/env bash
#
# exit-test.sh — the executable half of this delivery's exit criterion:
# a demonstration that the CLI works end to end, run in front of someone
# rather than hidden inside a test suite. Every step prints what it is
# about to prove before it asserts it.
#
# IT IS NOT PART OF `make ci`, AND THE REASON IS ITS SUBJECT rather than
# its cost. The gate reads this checkout; this reads a running service,
# a container runtime and a mail catcher, none of which a checkout has.
# What the gate does carry is every property that can be established
# without them, which is most of them.
#
# REQUIRE-UP, NEVER START-IT-OURSELVES. This script does not start the
# control plane, and that is deliberate: a script that launches the
# service it is testing owns that service's lifecycle, its logs and its
# shutdown, and every one of those is a way for an exit test to pass for
# a reason unconnected to the system. It asks, it refuses if the answer
# is wrong, and it names the command that fixes it.
#
# WHAT IT TAKES FROM ITS ENVIRONMENT, all of it overridable, none of it
# compiled in:
#
#   CURIOUS_API_URL       where the control plane answers
#   MAILPIT_URL           the mail catcher's HTTP API
#   API_ACCESS_LOG        the file the service's stdout was captured to
#   OPERATOR_LOGS         where this run's own transcript lands
#   OBJECT_STORE_URL      the object-store address the service signs with
#
# API_ACCESS_LOG is how the zero-network claim is MEASURED rather than
# asserted. The service already writes one line per request, at info
# level, carrying the routed pattern and the status — so the question
# "did this run touch the network" has an answer at the far end, written
# by something with no stake in this script's verdict. Capture that
# stdout to a file when the service is started; a measurement that lives
# in a terminal's scrollback has a half-life.
#
# A RECORDING PROXY WOULD NOT WORK HERE, which is worth stating so the
# next person does not spend the afternoon finding out: the toolchain
# returns no proxy for loopback as a special case, so a proxy would see
# nothing whether or not anything was sent, and a zero that cannot be
# told from "nothing was watching" is not evidence.
#
# THAT MEASUREMENT ASSUMES THIS RUN IS THE SERVICE'S ONLY CLIENT. The
# access log is a shared instrument and carries nothing that would let
# one client's lines be told from another's, so anything else talking to
# the same service while this runs is counted against whichever fixture
# happened to be running — and the row that must report zero reports
# whatever the other client sent. Run this against a stack nobody else
# is using.
#
# WHAT THIS DOES NOT PROVE, said plainly because implying coverage is
# worse than having none:
#
#   - It simulates a fresh machine with a fresh home directory. The one
#     thing that cannot prove is first-run behaviour against a REAL home
#     directory, which only a real first run on a real machine shows.
#   - It never answers the confirmation question interactively. Driving
#     the binary through pipes makes the run non-interactive by
#     construction, so what is demonstrated is that a question was
#     REACHED, not that answering it works.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

API="${CURIOUS_API_URL:-http://127.0.0.1:8080}"
MAILPIT="${MAILPIT_URL:-http://localhost:8025}"
ACCESS_LOG="${API_ACCESS_LOG:-}"
OBJECT_STORE="${OBJECT_STORE_URL:-}"

# --- the transcript -----------------------------------------------------
#
# The log is an instrument, so it is opened at creation rather than
# reconstructed afterwards. With no OPERATOR_LOGS given this still keeps
# one, in a temporary directory, and says where — writing it into the
# repository instead would put an untracked file under every guard that
# reads this tree.
if [ -z "${OPERATOR_LOGS:-}" ]; then
	OPERATOR_LOGS="$(mktemp -d)"
	echo "OPERATOR_LOGS was not set, so this run's transcript goes to ${OPERATOR_LOGS}."
fi
mkdir -p "$OPERATOR_LOGS"
TRANSCRIPT="${OPERATOR_LOGS}/exit-test-$(date -u +%Y%m%dT%H%M%SZ).log"
# The original streams are kept so the run can hand them back at the end
# and wait for the writer; see cleanup, where the reason is written down.
exec 3>&1 4>&2
exec > >(tee "$TRANSCRIPT") 2>&1
TEE_PID=$!
echo "transcript: ${TRANSCRIPT}"

WORK="$(mktemp -d)"
BIN="${WORK}/curious"
PASSES=()
TIMINGS=()

cleanup() {
	local ec=$?
	[ -n "${WORK:-}" ] && rm -rf "$WORK"
	echo
	echo "transcript written: ${TRANSCRIPT}"
	# THE RUN DOES NOT END UNTIL THE WRITER HAS FINISHED. `tee` here is a
	# process substitution, which is an independent process: exiting while
	# it still holds buffered output simply loses it. The lines most
	# likely to be lost are the LAST ones — which is precisely where the
	# failure that stopped the run is written, so the transcript of a
	# failed run was the one transcript missing its own verdict.
	#
	# Measured rather than reasoned about: a run that refused at its last
	# scenario wrote every PASS above it and no FAIL line at all.
	exec 1>&3 2>&4
	if [ -n "${TEE_PID:-}" ]; then
		wait "$TEE_PID" 2>/dev/null || true
	fi
	exit "$ec"
}
trap cleanup EXIT

# Every assertion failure goes through here, so it always says what was
# expected, what arrived, and which demonstration it belonged to. A bare
# `exit 1` teaches nothing to the person watching.
fail() {
	echo "FAIL [$1]: $2" >&2
	exit 1
}

pass() {
	PASSES+=("$1")
	echo "PASS: $1"
}

run_scenario() {
	local name="$1" fn="$2" start end
	echo
	echo "=== ${name} ==="
	start="$(date +%s)"
	"$fn"
	end="$(date +%s)"
	TIMINGS+=("${name}: $((end - start))s")
}

# --- helpers ------------------------------------------------------------

# isolate simulates a fresh machine, and the unset is the load-bearing
# half. The config-path override is resolved FIRST, ahead of the home
# directory and ahead of the base-directory variable, so a value
# inherited from the caller's shell silently defeats the whole
# simulation: the run would read somebody's real login and report a pass
# that says nothing. Scenario "isolation" below proves this unset works
# rather than trusting it.
isolate() {
	# THE TWO DECLARATIONS ARE SEPARATE STATEMENTS, and that is a
	# correctness requirement rather than a layout preference. `local` is
	# a command, so every one of its arguments is expanded BEFORE it runs
	# and before any of its assignments take effect — which means a
	# `${name}` written on the same line resolves against the CALLER's
	# `name`, not the one being declared beside it. Under bash's dynamic
	# scoping that caller is the scenario runner, whose own `name` holds
	# the scenario's title.
	#
	# Written as one line, this handed every machine in a scenario the
	# same directory, because the directory was named after the scenario
	# rather than after the machine. The row that suffered for it was the
	# one demonstrating that a second login ends the first machine's
	# session: both machines shared a config file, the second login
	# overwrote the first machine's credential with the new and valid
	# one, and the first machine then deployed perfectly — so the row
	# reported that a session had NOT ended, while actually testing one
	# machine twice.
	local name="$1"
	local dir="${WORK}/machine-${name}"
	mkdir -p "${dir}/.config"
	printf '%s' "$dir"
}

# curious_in runs the binary as a fresh machine would.
curious_in() {
	local home="$1"
	shift
	env -u CURIOUS_CONFIG \
		HOME="$home" \
		XDG_CONFIG_HOME="${home}/.config" \
		CURIOUS_API_URL="$API" \
		"$BIN" "$@"
}

# How long the answer's side of the terminal stays open after typing. It
# only has to outlast the program's walk of a fixture, which is
# milliseconds; the rest is margin for a loaded machine.
PTY_HOLD="${PTY_HOLD_SECONDS:-8}"
PTY_FLAVOUR="none"

# pty_answer runs the binary with a REAL TERMINAL on both its input and
# its diagnostics, and types one answer at the question it asks. Both
# have to be terminals before the program will ask anything at all, so a
# pipe cannot reach this path however it is fed.
#
# STDIN IS HELD OPEN AFTER THE ANSWER, and that is the mechanism rather
# than a flourish. Closing it immediately hands the program END-OF-INPUT
# before it has finished asking, and end-of-input with nothing typed is
# ITSELF a decline — so the run prints "cancelled" whatever was typed.
# Measured: an answer of "y" delivered through a pipe that closed at once
# cancelled the deploy and packed nothing, which is indistinguishable
# from a correct refusal. A declining row built that way is green without
# the answer ever being read.
#
# The defence is that both rows share this helper. If answers stopped
# arriving, the accepting row would cancel too and would red — so the
# pair cannot quietly rot one at a time.
pty_answer() {
	local answer="$1" home="$2"
	shift 2
	if [ "$PTY_FLAVOUR" = "gnu" ]; then
		# util-linux takes the command as one string after -c.
		local quoted
		quoted="$(printf '%q ' "$BIN" "$@")"
		{
			printf '%s\n' "$answer"
			sleep "$PTY_HOLD"
		} | env -u CURIOUS_CONFIG HOME="$home" XDG_CONFIG_HOME="${home}/.config" \
			CURIOUS_API_URL="$API" \
			script -qec "$quoted" /dev/null 2>&1 | tr -d '\r'
		return
	fi
	{
		printf '%s\n' "$answer"
		sleep "$PTY_HOLD"
	} | env -u CURIOUS_CONFIG HOME="$home" XDG_CONFIG_HOME="${home}/.config" \
		CURIOUS_API_URL="$API" \
		script -q /dev/null "$BIN" "$@" 2>&1 | tr -d '\r'
}

# log_mark and requests_since turn the service's own access log into a
# counter. The mark is a line count rather than a timestamp: two runs
# inside the same second are indistinguishable by time and are not
# indistinguishable by position.
log_mark() {
	if [ -n "$ACCESS_LOG" ] && [ -f "$ACCESS_LOG" ]; then
		wc -l <"$ACCESS_LOG" | tr -d ' '
	else
		echo 0
	fi
}

requests_since() {
	local mark="$1"
	if [ -z "$ACCESS_LOG" ] || [ ! -f "$ACCESS_LOG" ]; then
		echo 0
		return
	fi
	tail -n "+$((mark + 1))" "$ACCESS_LOG" | grep -c '"msg":"request"' || true
}

patterns_since() {
	local mark="$1"
	tail -n "+$((mark + 1))" "$ACCESS_LOG" |
		grep '"msg":"request"' |
		sed -n 's/.*"path":"\([^"]*\)".*/\1/p' || true
}

# mailpit_matching lists the catcher's messages for exactly one address,
# oldest first. The catcher's own query is a substring match rather than
# an equality, so an address that merely resembles this one would answer
# for it — hence the exact client-side filter on a sole recipient. -sS
# rather than -s so a catcher that is down produces a message rather than
# a silent non-zero exit that `set -e` turns into a bare failure.
mailpit_matching() {
	local to="$1" query
	query="$(jq -rn --arg s "to:$to" '$s|@uri')"
	curl -sS "${MAILPIT}/api/v1/messages?query=${query}" |
		jq -c --arg to "$to" \
			'[.messages[] | select((.To|length)==1 and .To[0].Address==$to)]
			 | sort_by(.Created)'
}

mailpit_count() {
	mailpit_matching "$1" | jq 'length' 2>/dev/null || echo 0
}

# mailpit_code waits for a message BEYOND the ones already there, and the
# baseline is the whole point rather than a refinement.
#
# The catcher keeps every message it has ever been handed, so a SECOND
# login for one address finds the first login's message sitting there
# immediately. Waiting on existence therefore returns a code that has
# already been spent, the verification that follows fails, and no second
# login happens at all — while every later row still reads as though one
# had. That is not hypothetical: it is what this script did on its first
# run, and the row it broke was the one demonstrating that a second login
# ends the first machine's session, which quietly tested nothing.
mailpit_code() {
	local to="$1" baseline="${2:-0}" matching count id text
	for _ in $(seq 1 60); do
		matching="$(mailpit_matching "$to")"
		count="$(jq 'length' <<<"$matching" 2>/dev/null || echo 0)"
		if [ "$count" -gt "$baseline" ]; then
			id="$(jq -r 'last | .ID' <<<"$matching")"
			break
		fi
		sleep 0.3
	done
	[ -z "${id:-}" ] && fail "login" "no message beyond the ${baseline} already held arrived \
for ${to} within 18s"
	text="$(curl -sS "${MAILPIT}/api/v1/message/${id}" | jq -r '.Text')"
	printf '%s' "$text" | grep -oE '[0-9]{6}' | head -n1
}

# login_as performs a login the way a person would, out of band, and
# hands back the credential. The CLI's own login asks two questions, and
# a run driven through pipes cannot answer them; what is being set up
# here is the STATE a completed login leaves, so that everything after
# it exercises the deploy path rather than the prompt.
login_as() {
	local email="$1" code token before
	# Counted BEFORE the code is asked for, so the wait below is for this
	# login's own message rather than for any message at all.
	before="$(mailpit_count "$email")"
	curl -sS -o /dev/null -X POST "${API}/v1/auth/start" \
		-H 'Content-Type: application/json' -d "{\"email\":\"${email}\"}"
	code="$(mailpit_code "$email" "$before")"
	token="$(curl -sS -X POST "${API}/v1/auth/verify" \
		-H 'Content-Type: application/json' \
		-d "{\"email\":\"${email}\",\"code\":\"${code}\"}" | jq -r '.token // empty')"
	# A login that produced no credential says so HERE. Handing an empty
	# string back would leave every row after it asserting against a
	# machine that never logged in, and the row would still report
	# whatever it found — which is how a broken setup gets mistaken for
	# the property under test.
	[ -n "$token" ] ||
		fail "login" "logging in as ${email} produced no credential"
	printf '%s' "$token"
}

seed_login() {
	local home="$1" token="$2"
	# AN EMPTY CREDENTIAL IS REFUSED HERE, at the one place every machine
	# is set up, because `fail` cannot stop this script from inside a
	# command substitution: it exits that subshell and the caller carries
	# on with an empty string. A machine seeded with nothing then gets
	# refused by the service for the ordinary reason, and a row asserting
	# a refusal would PASS while demonstrating nothing whatsoever.
	[ -n "$token" ] ||
		fail "setup" "a machine was about to be set up with an empty credential, which \
means a login failed earlier and said so somewhere this run did not stop for"
	mkdir -p "${home}/.config/curious"
	printf '{"api_url":"%s","token":"%s","version":1}\n' "$API" "$token" \
		>"${home}/.config/curious/config.json"
	chmod 600 "${home}/.config/curious/config.json"
}

# --- setup --------------------------------------------------------------

setup() {
	local missing=()
	for tool in jq curl tar awk node go docker shasum script; do
		command -v "$tool" >/dev/null 2>&1 || missing+=("$tool")
	done
	# Every missing tool at once. Naming only the first costs the reader a
	# second run to discover the second.
	[ ${#missing[@]} -gt 0 ] &&
		fail "setup" "these programs are needed and are not on the path: ${missing[*]}"

	local status
	status="$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "${API}/v1/capacity" 2>/dev/null || true)"
	# curl prints its placeholder AND exits non-zero on a connection
	# failure, so the obvious `|| echo 000` concatenates into a status
	# nothing ever returned. Fall back only when curl printed nothing.
	[ -z "$status" ] && status="000"
	if [ "$status" != "200" ]; then
		fail "setup" "${API}/v1/capacity did not answer 200 (got ${status}). This script \
REQUIRES the control plane already running and never starts it itself — see this file's header. \
Bring the local stack up, create its table, then start the service with its stdout captured to a \
file, and point API_ACCESS_LOG at that file."
	fi

	if [ -z "$ACCESS_LOG" ]; then
		fail "setup" "API_ACCESS_LOG is not set. The zero-network rows below MEASURE what \
was sent, at the service's own access log; without it they would assert a zero they never looked \
for, which is the one result this test must not produce."
	fi
	[ -f "$ACCESS_LOG" ] ||
		fail "setup" "API_ACCESS_LOG is ${ACCESS_LOG}, which does not exist."

	go build -trimpath -o "$BIN" ./cmd/curious ||
		fail "setup" "the binary under test did not build"

	pass "setup — the service answers, the access log is readable, and the binary built"
}

# --- isolation ----------------------------------------------------------

scenario_isolation() {
	echo "Proving the fresh-machine simulation is not defeated by an inherited setting."
	local home poison
	home="$(isolate isolation)"
	poison="${WORK}/inherited-config.json"
	printf '{"api_url":"%s","token":"%s","version":1}\n' "$API" "inherited-never-used" >"$poison"

	# The override is exported into this run deliberately. It is resolved
	# ahead of everything else, so if the helper did not unset it the
	# binary would read THIS file and the fresh directory would be
	# scenery.
	export CURIOUS_CONFIG="$poison"

	local email token
	email="isolation-$(date +%s)-$$@example.com"
	token="$(login_as "$email")"
	seed_login "$home" "$token"

	local out
	out="$(curious_in "$home" deploy testdata/projects/astro-absent 2>&1 || true)"
	unset CURIOUS_CONFIG

	grep -q 'astro-dep-absent' <<<"$out" ||
		fail "isolation" "the isolated run did not reach the project check; got: ${out}"

	# The proof: the inherited file is untouched, and the fresh directory
	# is what the run actually used.
	grep -q 'inherited-never-used' "$poison" ||
		fail "isolation" "the inherited config file was rewritten, so the override was in force"
	[ -f "${home}/.config/curious/config.json" ] ||
		fail "isolation" "the fresh directory holds no config, so it is not what the run read"

	pass "isolation — with the config-path override exported, the fresh directory still wins"
}

# --- 1) the project is checked before anything is sent ------------------

scenario_preflight() {
	echo "Four broken projects stop with a message naming the fix, and each one sends nothing."

	# The counter's own positive control comes FIRST. A zero that cannot
	# be told from "nothing was watching" is not evidence, so before any
	# zero is believed, the same counter is made to report a one.
	local mark seen
	mark="$(log_mark)"
	curl -sS -o /dev/null "${API}/v1/capacity"
	seen="$(requests_since "$mark")"
	[ "$seen" -ge 1 ] ||
		fail "pre-flight" "the access-log counter reported ${seen} for a request that was \
definitely made, so every zero it reports below would be meaningless"
	pass "pre-flight — control: the access-log counter sees a request that was made"

	local home
	home="$(isolate preflight)"

	# fixture:failure id — four projects, four different reasons to stop.
	local case fixture want out rc
	for case in \
		"astro-absent:astro-dep-absent" \
		"no-package-json:astro-dep-missing" \
		"malformed-package-json:astro-dep-invalid-json" \
		"lockfile-yarn-only:lockfile-unsupported"; do
		fixture="${case%%:*}"
		want="${case##*:}"
		mark="$(log_mark)"
		set +e
		out="$(curious_in "$home" deploy "testdata/projects/${fixture}" 2>&1 </dev/null)"
		rc=$?
		set -e

		[ "$rc" -eq 1 ] ||
			fail "pre-flight" "${fixture} exited ${rc}, want 1"
		grep -q "Failure ID: ${want}" <<<"$out" ||
			fail "pre-flight" "${fixture} did not report ${want}; got: ${out}"
		# A hard stop that does not name an action is copy nobody can act
		# on, and this is the whole claim being demonstrated.
		grep -qE 'curious deploy \./my-site|npm install|pnpm install|Open package\.json' <<<"$out" ||
			fail "pre-flight" "${fixture} named no action the reader can take; got: ${out}"

		seen="$(requests_since "$mark")"
		[ "$seen" -eq 0 ] ||
			fail "pre-flight" "${fixture} stopped locally but the service logged ${seen} \
request(s): $(patterns_since "$mark" | tr '\n' ' ')"

		pass "pre-flight — ${fixture} stops with ${want}, names the fix, and sends nothing"
	done

	echo
	echo "And a project whose srcDir is computed, with no pages directory, is asked about rather than refused."
	mark="$(log_mark)"
	set +e
	out="$(curious_in "$home" deploy testdata/projects/computed-srcdir 2>&1 </dev/null)"
	rc=$?
	set -e

	grep -q "couldn't read srcDir out of astro.config.mjs" <<<"$out" ||
		fail "pre-flight" "the computed srcDir produced no warning about it; got: ${out}"
	# The discriminator between "warned" and "stopped": a stop prints a
	# project failure and never asks anything. Reaching the question is
	# what proves this was a warning, and a run driven through pipes has
	# no way to answer, which is why the question is where it ends.
	grep -q 'Failure ID: needs-a-terminal' <<<"$out" ||
		fail "pre-flight" "the computed srcDir did not reach a question; got: ${out}"
	if grep -qE 'Failure ID: (astro-dep|lockfile|project-not-ready)' <<<"$out"; then
		fail "pre-flight" "the computed srcDir hard-stopped, and it must not; got: ${out}"
	fi

	seen="$(requests_since "$mark")"
	[ "$seen" -eq 0 ] ||
		fail "pre-flight" "the computed srcDir sent ${seen} request(s) before asking"

	pass "pre-flight — a computed srcDir warns and asks rather than stopping, and sends nothing"
}

# --- 2) a project that can deploy takes the whole path ------------------

# The object store is reached from two places that do not share a network
# namespace: this machine, which uploads, and the per-build container,
# which downloads. The default is loopback, which is right for the first
# and unreachable from the second — a container's own loopback is itself.
# So the address the service signs with has to answer from BOTH, and this
# refuses rather than discovering it as an extraction failure several
# minutes later, which is a message about the site source that is really
# a message about networking.
object_store_reachable_from_container() {
	docker run --rm --entrypoint sh "${CURIOUS_BUILDER_IMAGE:-curious-builder:dev}" \
		-c "curl -s -m 5 -o /dev/null -w '%{http_code}' ${1}/ 2>/dev/null" 2>/dev/null
}

scenario_deploy() {
	echo "A project that can deploy goes all the way to the service's own answer."

	[ -n "$OBJECT_STORE" ] ||
		fail "deploy" "OBJECT_STORE_URL is not set, so the per-build container has no \
address to fetch the source from. It must be the same address the service was started with, \
because the service signs the upload link and the container follows it."

	case "$OBJECT_STORE" in
	*//localhost:* | *//127.* | *//\[::1\]:*)
		fail "deploy" "OBJECT_STORE_URL is ${OBJECT_STORE}, a loopback address. It is \
correct for this machine and unreachable from the per-build container, whose own loopback is \
itself — so every build would fail while downloading the source."
		;;
	esac

	local host_status container_status
	host_status="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "${OBJECT_STORE}/" 2>/dev/null || true)"
	[ -z "$host_status" ] && host_status="000"
	# `|| true` IS LOAD BEARING, and its absence defeated the whole check.
	# An unreachable address is what this probe EXISTS to find, and an
	# unreachable address is exactly what makes the container's own client
	# exit non-zero — which the container runtime then reports as its own
	# exit code. Without this the assignment fails, the run dies of that
	# status, and the careful refusal below never prints: the operator
	# gets a bare non-zero exit instead of the sentence naming the fix.
	# Measured: this script exited 28, a client timeout, rather than
	# saying a container could not reach the object store.
	container_status="$(object_store_reachable_from_container "$OBJECT_STORE" || true)"
	[ -z "$container_status" ] && container_status="000"

	if [ "$host_status" = "000" ] || [ "$container_status" = "000" ]; then
		fail "deploy" "the object store at ${OBJECT_STORE} must answer from BOTH this \
machine and a container, and it does not: this machine got ${host_status}, a container got \
${container_status}. One address has to satisfy both, because this machine uploads the archive \
and the per-build container downloads it.
    THE FIX IS ONE LINE AND IT IS AN OPERATOR ACTION, which is why this names it rather than \
doing it: add a hosts entry mapping the container runtime's name for this machine to loopback, \
in /etc/hosts, needing sudo —
        127.0.0.1 host.docker.internal
    then set OBJECT_STORE_URL to that name and start the service with the SAME value, since the \
service signs the upload link and the container follows it. That one name then answers from \
here, where the object store is published, and from inside a container, which is the pair of \
answers no other address on this machine gave."
	fi
	pass "deploy — the object store answers from this machine (${host_status}) and from a container (${container_status})"

	# The project is built here, at run time, rather than committed. A
	# real lockfile is the dependency graph of a whole framework written
	# out; committed it would be a large generated file in a public tree,
	# carrying thousands of names and digests nobody here chose.
	local site="${WORK}/site"
	mkdir -p "${site}/src/pages"
	cat >"${site}/package.json" <<'JSON'
{
  "name": "exit-test-site",
  "version": "0.0.0",
  "private": true,
  "type": "module",
  "scripts": { "build": "astro build" },
  "dependencies": { "astro": "7.2.4" }
}
JSON
	printf 'import { defineConfig } from "astro/config";\nexport default defineConfig({});\n' \
		>"${site}/astro.config.mjs"
	printf -- '---\nconst title = "exit test";\n---\n<h1>{title}</h1>\n' \
		>"${site}/src/pages/index.astro"

	echo "Resolving a real lockfile for the project (no lockfile is committed for this)."
	(cd "$site" && npm install --package-lock-only --no-audit --no-fund >/dev/null 2>&1) ||
		fail "deploy" "could not resolve a lockfile for the generated project"
	jq -e '.packages["node_modules/astro"].version' "${site}/package-lock.json" >/dev/null ||
		fail "deploy" "the generated lockfile pins no astro, so the build would have nothing to run"

	local home email token mark out rc
	home="$(isolate deploy)"
	email="deploy-$(date +%s)-$$@example.com"
	token="$(login_as "$email")"
	seed_login "$home" "$token"

	mark="$(log_mark)"
	set +e
	out="$(curious_in "$home" deploy "$site" 2>&1 </dev/null)"
	rc=$?
	set -e
	echo "$out"

	grep -q 'Streaming the build log' <<<"$out" ||
		fail "deploy" "the run never reached the build log; got: ${out}"

	# The local stack has no edge key-value store to write the routing
	# entry into, so publishing cannot succeed here and says so. That is
	# the honest end of this path: a local success that could not be true
	# anywhere would teach that the loop works when it does not. What is
	# being demonstrated is that the client renders the SERVICE's answer
	# rather than inventing a cause of its own.
	local seq
	seq="$(patterns_since "$mark" | tr '\n' ' ')"
	echo "the service recorded: ${seq}"
	for want in /v1/deploys "/v1/deploys/{id}/start" "/v1/deploys/{id}/events"; do
		grep -q -- "$want" <<<"$seq" ||
			fail "deploy" "the service never recorded ${want}; it recorded: ${seq}"
	done

	pass "deploy — the archive uploaded, the build ran, and the service recorded the whole sequence"
}

# --- the question, asked where it can be answered -----------------------

scenario_prompt() {
	echo "With a real terminal the question is asked out loud, and both answers are honoured."

	if script -q /dev/null true >/dev/null 2>&1; then
		PTY_FLAVOUR=bsd
	elif script -qec true /dev/null >/dev/null 2>&1; then
		PTY_FLAVOUR=gnu
	else
		fail "the question" "no 'script' on this machine can allocate a terminal in \
either the BSD or the util-linux form, so these rows cannot run. They are NOT skipped and NOT \
passed: a check that quietly does not run looks exactly like one that did."
	fi

	local home out
	home="$(isolate declined)"
	out="$(pty_answer n "$home" deploy testdata/projects/computed-srcdir || true)"

	grep -q 'Continue anyway? \[Y/n\]' <<<"$out" ||
		fail "the question" "with a terminal the question was never asked; got: ${out}"
	# THE STOP IS ASSERTED AS THE THING THAT DID NOT HAPPEN, rather than
	# as a label for it. Declining is not a failure: it mints no failure
	# id, prints "cancelled" and exits 0, because a person saying no has
	# not hit a fault. So what proves the run stopped is that no archive
	# was ever built — which is the observable the row is really about,
	# and a stronger claim than any name for it would be.
	if grep -q 'Packed' <<<"$out"; then
		fail "the question" "declining still packed the project; got: ${out}"
	fi
	grep -q 'cancelled' <<<"$out" ||
		fail "the question" "declining did not say so; got: ${out}"
	pass "the question — with a terminal it is asked, and declining stops before anything is packed"

	# Accepting needs a stored credential, because the question is asked
	# BEFORE the login step: a machine with no login would answer this
	# one and then stop at a question no pipe can answer.
	local ahome email token
	ahome="$(isolate accepted)"
	email="question-$(date +%s)-$$@example.com"
	token="$(login_as "$email")"
	seed_login "$ahome" "$token"

	out="$(pty_answer y "$ahome" deploy testdata/projects/computed-srcdir || true)"
	grep -q 'Continue anyway? \[Y/n\]' <<<"$out" ||
		fail "the question" "the question was not asked before accepting; got: ${out}"
	grep -qE 'Packed [0-9]+ files' <<<"$out" ||
		fail "the question" "accepting did not carry on into the pack; got: ${out}"
	pass "the question — accepting carries on into the pack"

	# THE PAIR'S OWN CONTROL. If answers were not reaching the program,
	# the terminal would hand it end-of-input, which reads as a decline —
	# so this row would have cancelled and packed nothing, and the
	# declining row above would have been asserting a stop it never
	# caused. One of the two cannot break without this saying so.
	if grep -q 'cancelled' <<<"$out"; then
		fail "the question" "accepting was read as a decline, so the answer is not \
reaching the program and the declining row above proves nothing"
	fi
	pass "the question — control: accepting is not read as a decline, so both answers are really read"
}

# --- 4) the release pipeline, proved without publishing anything --------

scenario_pipeline() {
	echo "The release pipeline builds every artefact and publishes none of them."

	make snapshot >"${WORK}/snapshot.log" 2>&1 ||
		fail "pipeline" "make snapshot failed; see ${WORK}/snapshot.log"
	grep -q 'each one the wrapper knows how to ask for' "${WORK}/snapshot.log" ||
		fail "pipeline" "the built archives and the names the wrapper asks for did not agree"
	pass "pipeline — every artefact built, and each is one the wrapper knows how to ask for"

	# Verified from the artefact directory, the way the page tells a
	# person to verify a download.
	(cd dist && shasum -a 256 -c checksums.txt --ignore-missing >/dev/null 2>&1) ||
		fail "pipeline" "the checksums did not verify against the artefacts that were built"

	# And the control: a checksum check that cannot fail is not a check.
	local victim="dist/checksums.txt"
	cp "$victim" "${WORK}/checksums.orig"
	local archive
	archive="$(ls dist/*.gz | grep -v '.tar.gz' | head -n1)"
	cp "$archive" "${WORK}/archive.orig"
	printf 'tampered' >>"$archive"
	if (cd dist && shasum -a 256 -c checksums.txt --ignore-missing >/dev/null 2>&1); then
		cp "${WORK}/archive.orig" "$archive"
		fail "pipeline" "a tampered archive still verified, so the verification proves nothing"
	fi
	cp "${WORK}/archive.orig" "$archive"
	(cd dist && shasum -a 256 -c checksums.txt --ignore-missing >/dev/null 2>&1) ||
		fail "pipeline" "the archive was not restored after the tamper control"
	pass "pipeline — checksums verify, and a tampered archive fails them"

	# The installable path is the wrapper's own end-to-end target rather
	# than a second description of it here. Two spellings of one
	# procedure drift the day one of them is edited.
	make e2e-npm >"${WORK}/e2e.log" 2>&1 ||
		fail "pipeline" "the wrapper's end-to-end install failed; see ${WORK}/e2e.log"
	grep -q 'packed, installed, verified against the embedded digest, and ran' "${WORK}/e2e.log" ||
		fail "pipeline" "the wrapper's end-to-end run did not report a complete install"
	pass "pipeline — the package installs from a tarball and the command it installs runs"

	# A machine with no build gets a sentence rather than a download that
	# 404s several seconds later. Simulated, because the refusal is about
	# a platform this run is not on.
	local refusal
	refusal="$(node -e '
		const p = require("./npm/lib/platform.js");
		if (p.assetName("1.2.3", "linux", "s390x") !== null) {
			console.error("an unsupported platform was given an archive name");
			process.exit(1);
		}
		process.stdout.write(p.unsupportedMessage("linux", "s390x"));
	')" || fail "pipeline" "the unsupported-platform path did not refuse"
	grep -q 'no prebuilt binary for linux/s390x' <<<"$refusal" ||
		fail "pipeline" "the refusal does not name the machine it refused; got: ${refusal}"
	grep -q 'build it yourself' <<<"$refusal" ||
		fail "pipeline" "the refusal names no way forward; got: ${refusal}"
	pass "pipeline — an unsupported platform is refused in words, with a way forward"
}

# --- 5) a second login ends the first machine's session -----------------

scenario_second_login() {
	echo "Logging in again on a second machine ends the first machine's session."

	local first second email token_one out rc
	first="$(isolate first)"
	second="$(isolate second)"
	email="two-machines-$(date +%s)-$$@example.com"

	token_one="$(login_as "$email")"
	seed_login "$first" "$token_one"

	# THE POSITIVE CONTROL COMES FIRST, and it is the whole reason this
	# is one row in two halves. A token that never worked is refused in
	# exactly the same words as a token that has just been ended, so a
	# refusal on its own demonstrates nothing at all. What makes the
	# second half mean something is having watched the same credential
	# work, moments earlier, with only a login in between.
	set +e
	out="$(curious_in "$first" deploy testdata/projects/valid 2>&1 </dev/null)"
	rc=$?
	set -e
	grep -q 'The server says this deploy is building' <<<"$out" ||
		fail "second login" "the first machine's credential did not get past the service; got: ${out}"
	pass "second login — the first machine's credential is accepted and its deploy is taken"

	# Nothing between the two halves but a login.
	local token_two
	token_two="$(login_as "$email")"
	seed_login "$second" "$token_two"
	[ "$token_one" != "$token_two" ] ||
		fail "second login" "the second login handed back the same credential, so nothing changed"

	set +e
	out="$(curious_in "$first" deploy testdata/projects/valid 2>&1 </dev/null)"
	rc=$?
	set -e
	if grep -q 'The server says this deploy is building' <<<"$out"; then
		fail "second login" "the first machine's credential still works after the second login"
	fi
	# Refused means sent back to the beginning: the run reaches the login
	# it cannot complete through a pipe.
	grep -q 'Failure ID: needs-a-terminal' <<<"$out" ||
		fail "second login" "the first machine was not sent back to a login; got: ${out}"
	pass "second login — the same credential is refused afterwards, with only a login in between"

	# And the reason the order above is not a stylistic choice.
	local never
	never="$(isolate never)"
	seed_login "$never" "a-credential-that-never-worked"
	set +e
	out="$(curious_in "$never" deploy testdata/projects/valid 2>&1 </dev/null)"
	set -e
	grep -q 'Failure ID: needs-a-terminal' <<<"$out" ||
		fail "second login" "a credential that never worked produced some other outcome; got: ${out}"
	pass "second login — control: a credential that never worked is refused identically"
}

# --- 7) the agent surface, over the protocol ----------------------------

# frames_are_well_formed reads a captured stream and answers whether
# every byte of it belongs to a well-formed protocol frame.
#
# IT IS A PARSE, NOT A SEARCH, and that distinction is the point. The
# server folds its narration and the build's own output INTO its results
# deliberately, so those words appear inside frames as legitimate
# content: a search for them reds a healthy run, and a search for
# anything else cannot see a stray write that happens to look like
# nothing in particular. Asking instead whether every line is a frame
# answers the real question, which is about origin.
frames_are_well_formed() {
	local file="$1"
	[ -s "$file" ] || return 1
	# A final write that was cut off mid-frame leaves no closing newline.
	[ "$(tail -c1 "$file" | wc -l | tr -d ' ')" = "1" ] || return 1
	local line
	while IFS= read -r line; do
		[ -n "$line" ] || return 1
		jq -e 'if .jsonrpc != "2.0" then false
		       elif has("id") then (has("result") or has("error"))
		       else has("method") end' >/dev/null 2>&1 <<<"$line" || return 1
	done <"$file"
	return 0
}

scenario_agent() {
	echo "The agent surface, driven as a client drives it, over the protocol."

	local home email out err fifo pid
	home="$(isolate agent)"
	email="agent-$(date +%s)-$$@example.com"
	out="${WORK}/agent.out"
	err="${WORK}/agent.err"
	fifo="${WORK}/agent.in"
	mkfifo "$fifo"

	# One session for all four tools: the login has to complete before
	# the deploy can be asked for, and the deploy's own answer carries
	# the id the status call needs.
	env -u CURIOUS_CONFIG \
		HOME="$home" XDG_CONFIG_HOME="${home}/.config" CURIOUS_API_URL="$API" \
		"$BIN" mcp <"$fifo" >"$out" 2>"$err" &
	pid=$!
	exec 9>"$fifo"

	send() { printf '%s\n' "$1" >&9; }
	await() {
		local id="$1" limit="${2:-300}" waited=0
		while [ "$waited" -lt "$limit" ]; do
			if jq -s -e --argjson i "$id" 'any(.[]; .id==$i)' "$out" >/dev/null 2>&1; then
				return 0
			fi
			sleep 1
			waited=$((waited + 1))
		done
		fail "agent" "no answer to request ${id} within ${limit}s; stderr: $(cat "$err")"
	}
	reply() { jq -s -r --argjson i "$1" '[.[] | select(.id==$i)][0] | .result.content[0].text' "$out"; }

	send '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}'
	await 1
	jq -s -e '[.[] | select(.id==1)][0].result.serverInfo.name == "curious"' "$out" >/dev/null ||
		fail "agent" "the handshake did not identify the server"
	pass "agent — the handshake completes"

	send '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
	await 2
	# Set equality, so a tool that quietly disappears is as loud as one
	# that quietly arrives.
	local listed
	listed="$(jq -s -r '[.[] | select(.id==2)][0].result.tools | map(.name) | sort | join(",")' "$out")"
	[ "$listed" = "deploy_site,deploy_status,login_start,login_verify" ] ||
		fail "agent" "the tool list is ${listed}, want exactly the four"
	pass "agent — the four tools are listed, as a set"

	send "$(printf '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"login_start","arguments":{"email":"%s"}}}' "$email")"
	await 3
	local code
	code="$(mailpit_code "$email")"
	send "$(printf '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"login_verify","arguments":{"email":"%s","code":"%s"}}}' "$email" "$code")"
	await 4
	jq -s -e '[.[] | select(.id==4)][0].result.isError == false' "$out" >/dev/null ||
		fail "agent" "the login was refused: $(reply 4)"
	[ -f "${home}/.config/curious/config.json" ] ||
		fail "agent" "the login left no stored credential"
	pass "agent — login_start and login_verify complete, and the credential is stored"

	send "$(printf '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"deploy_site","arguments":{"dir":"%s"}}}' "${repo_root}/testdata/projects/valid")"
	await 5 600
	# The deploy is asked for, and its answer carries the id either way:
	# a taken deploy returns it as a field, a refused one names it in the
	# text, because the point of returning it is that the caller can ask
	# what happened next.
	local deploy_id
	deploy_id="$(jq -s -r '[.[] | select(.id==5)][0].result.content[0].text' "$out" |
		jq -r '.deploy_id // empty' 2>/dev/null || true)"
	if [ -z "$deploy_id" ]; then
		# The id is taken WITHOUT the sentence's full stop. A character
		# class holding a dot swallows it, and the status call then asks
		# about an id that does not exist — a red that names the wrong
		# thing entirely.
		deploy_id="$(reply 5 | sed -n 's/.*The deploy id is \([A-Za-z0-9_-]*\).*/\1/p' | head -n1)"
	fi
	[ -n "$deploy_id" ] ||
		fail "agent" "deploy_site returned no deploy id to ask about: $(reply 5)"
	pass "agent — deploy_site ran and answered with a deploy id"

	send "$(printf '{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"deploy_status","arguments":{"deploy_id":"%s"}}}' "$deploy_id")"
	await 6 600
	jq -s -e '[.[] | select(.id==6)][0].result.content[0].text | fromjson | has("reported")' "$out" >/dev/null ||
		fail "agent" "deploy_status did not answer in its documented shape: $(reply 6)"
	pass "agent — deploy_status answers for the id deploy_site returned"

	exec 9>&-
	wait "$pid" || true

	frames_are_well_formed "$out" ||
		fail "agent" "something on the protocol stream was not a well-formed frame"
	pass "agent — every byte the server wrote belongs to a well-formed frame"

	# The control for the assertion just made. A stream checker that
	# passes a dirtied stream has been proving nothing all along, and the
	# two dirt shapes below are the realistic ones: a line that is not a
	# frame, and a write glued onto a frame with no newline between.
	cp "$out" "${WORK}/dirty-line"
	echo 'a stray line that is not a frame' >>"${WORK}/dirty-line"
	if frames_are_well_formed "${WORK}/dirty-line"; then
		fail "agent" "the frame check passed a stream with a stray line in it"
	fi

	printf '%s' "$(cat "$out")" >"${WORK}/dirty-glued"
	printf 'glued on with no newline' >>"${WORK}/dirty-glued"
	if frames_are_well_formed "${WORK}/dirty-glued"; then
		fail "agent" "the frame check passed a stream whose last write was not a frame"
	fi
	pass "agent — control: the frame check rejects a stray line and an unterminated write"
}

# --- run ----------------------------------------------------------------

echo "=== this run's inputs ==="
echo "  service:      ${API}"
echo "  mail catcher: ${MAILPIT}"
echo "  access log:   ${ACCESS_LOG:-<unset>}"
echo "  object store: ${OBJECT_STORE:-<unset>}"
echo
echo "A fresh machine is SIMULATED here, with a fresh home directory and"
echo "an explicitly unset config-path override. It is a simulation, and"
echo "the one thing it cannot show is first-run behaviour against a real"
echo "home directory."

run_scenario "setup" setup
run_scenario "isolation" scenario_isolation
run_scenario "1) the project is checked before anything is sent" scenario_preflight
# The question rows sit OUTSIDE scenario 1 deliberately: accepting
# carries on into a real deploy and therefore sends things, and scenario
# 1's whole claim is that nothing is sent.
run_scenario "the question a terminal can answer" scenario_prompt
run_scenario "4) the release pipeline" scenario_pipeline
run_scenario "5) a second login ends the first machine's session" scenario_second_login
run_scenario "7) the agent surface, over the protocol" scenario_agent

# THE CONTAINERISED PATH RUNS LAST, and the ordering is the same
# principle the client itself follows: everything free and local first.
# It is the only scenario that needs a container runtime and an address
# the per-build container can reach, so it is the only one that can be
# refused by the shape of the machine rather than by the code under
# test. Running it first would mean a machine missing that one
# precondition demonstrated nothing at all, when in fact everything
# above it holds and has just been shown to.
run_scenario "2) a project that can deploy takes the whole path" scenario_deploy

echo
echo "=== EXIT TEST PASSED — ${#PASSES[@]} checks ==="
for p in "${PASSES[@]}"; do
	echo "  - ${p}"
done
echo
echo "=== timings ==="
for t in "${TIMINGS[@]}"; do
	echo "  - ${t}"
done
