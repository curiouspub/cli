# make ci is the test gate: every check that can fail a change runs
# inside it, so what passes locally and what passes in CI cannot diverge.
#
# TWO TARGETS SIT OUTSIDE IT and each says why where it is defined. The
# short of it: one needs a tool this module cannot require, and the other
# needs a push range, which a working copy does not have.

export CGO_ENABLED := 0

.PHONY: build test test-go test-npm test-race e2e-npm vet fmt lint snapshot surface-check hooks ci

build:
	go build -trimpath ./...
	go build -trimpath -o bin/curious ./cmd/curious

# test grows as suites arrive that go test cannot see on its own (a
# release-build check, a wrapper package's own test runner, a pass under
# the race detector) — each lands here as an additional half, so one
# command still sees the whole repository.
#
# -count=1 disables the test cache, and it is LOAD BEARING rather than a
# habit. The guards in internal/guard read state Go does not track as an
# input to their package: the whole module's source tree, its dependency
# graph, and a manifest file. Go's cache key covers the guard package's
# own files, so a violation introduced anywhere else leaves the cached
# PASS valid and `make ci` reports green against a tree that breaks the
# rule.
#
# That is not hypothetical — it was measured. A banned telemetry import
# added to a test file went undetected on a cached run and failed
# immediately with -count=1. A guard that can report a stale pass is
# worse than no guard, because it is trusted.
#
# THE SUITE RUNS THROUGH A WRAPPER, and the reason is the same shape one
# line up: a run can report a pass it has not earned. A skipped row
# prints NOTHING without -v, so a green tick over a row that stopped
# running looks exactly like a green tick over one that passed — and
# several rows here skip on conditions that are honest on one platform
# and a broken environment on another. The wrapper prints every skip with
# the reason its author wrote, checks it against
# scripts/expected-skips.txt, and fails the run on one nobody declared.
# It passes its arguments straight through and returns the same exit
# code; what it adds is the section at the end.
#
# Making the whole suite verbose would surface the same three lines
# inside ten thousand, which is a way of hiding them that also annoys
# everybody.
# EVERY HALF RUNS, AND THE RESULT IS THE AGGREGATE. This was a
# prerequisite list until the race pass arrived, and a prerequisite list stops at the
# first failure — which defeats the sentence above it. The moment the
# stall-window registry reds on a leg nobody has measured yet, which is
# its designed state, make would stop and the race pass beside it would
# never run at all. On the leg that is pending, that is exactly the run
# whose output somebody needs.
#
# So the halves are invoked in sequence and the status is collected.
# THE RACE PASS GOES FIRST for a reason worth stating: the stall windows
# are margins over a gap the detector widens — threefold on darwin — so
# the number a pending leg has to report is the one taken under it, and a
# run that stopped before the race pass would hand an operator the
# friendlier of two figures with nothing on the line to say which it was.
test:
	@status=0; \
	$(MAKE) test-race || status=1; \
	$(MAKE) test-go || status=1; \
	$(MAKE) test-npm || status=1; \
	exit $$status

test-go:
	go run ./tools/skipcheck -- -count=1 ./...

# THE RACE PASS OVER THE STALL-WINDOW MACHINERY, and it is reached from
# test so that CI gets it without a second entry point: the workflow runs
# `make ci` and nothing else, so a check that is not reachable from here
# is a check CI does not run.
#
# WHY IT NAMES internal/flow AND NOT ONLY internal/timing. The ruling
# that produced this target says "the timing package's CI invocation",
# and internal/timing on its own would catch NOTHING: it is a registry
# and two AST guards, and it starts no goroutine. The data race that
# invalidated a whole round of measurements lived one package over — a
# receive-buffer size written onto the object-store fixture AFTER its
# listener had started accepting, read by the accept path — so the
# package that has to be under the detector is the one with the fixture
# in it. Naming only the registry would be a gate in the shape of the
# rule with none of its subject.
#
# WHAT IT COSTS AND WHAT THAT BUYS. internal/flow is the slowest package
# here, because five of its rows are timing probes that pace real bytes
# over loopback; under the detector it is about the same wall time, since
# the cost is sleeps rather than instructions. The gate therefore runs
# that package twice. It is worth it: the defect this catches is one that
# leaves every row GREEN and every number wrong, which is the only kind
# of defect a test suite cannot report on its own.
#
# THE STALL WINDOWS ARE SIZED UNDER THIS CONDITION. A margin has to hold
# in every condition the gate runs the row in, and the detector is one of
# them — see internal/timing, where each leg's number names the detector
# it was taken under.
#
# CGO_ENABLED=1 because the race detector needs a C toolchain on most
# platforms. The file-level export sets it to 0 for every other target,
# which is deliberate; this is the one place that has to differ, and it
# differs in the recipe rather than by moving the default.
# -timeout IS EXPLICIT AND IT IS NOT A ROUND NUMBER PULLED OUT OF THE
# AIR. Go defaults to ten minutes per test binary, and internal/flow
# under the detector on a two-core CI runner does not fit: the linux leg
# panicked at exactly 10m0s inside the upload probe, having produced no
# measurement at all, which is the worst of both — the money spent and no
# number back. The package takes about two minutes there without the
# detector and something over five times that with it.
test-race:
	CGO_ENABLED=1 go test -race -count=1 -timeout 25m ./internal/timing/ ./internal/flow/

# The wrapper package's own suite. It is a PREREQUISITE OF test rather
# than a separate command somebody has to know about, because a check
# outside the gate is a check nobody runs before pushing — and this one
# covers a postinstall script that downloads and executes a binary on
# other people's machines.
#
# IT FAILS RATHER THAN SKIPS when Node is absent, and that is the same
# ruling as the skip manifest one line up: a check that quietly does not
# run looks exactly like one that passed. The floor is the package's own
# declared engines range, and the suite reports what it found.
#
# The pattern is quoted so the glob reaches the test runner rather than
# the shell, and it is a glob rather than a directory because a
# directory argument is not one the runner accepts.
test-npm:
	@command -v node >/dev/null 2>&1 || { \
		echo "node is not installed, and the npm wrapper's suite is part of this gate."; \
		echo "Install Node (the floor is in npm/package.json), or run make test-go for the Go half."; \
		exit 1; \
	}
	cd npm && node --test "test/**/*.test.js"

# The end-to-end run: pack the wrapper, serve a built binary from this
# machine, install the tarball into a temporary prefix and run the
# command it installs. It is NOT part of ci: it builds the real binary
# and stands up a server, which is minutes rather than seconds.
e2e-npm:
	npm/test/e2e-local.sh

vet:
	go vet ./...

fmt:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "$$unformatted"; \
		exit 1; \
	fi

# lint is a no-op until a linter is actually configured for this repo —
# it must never fail ci for the reason "no linter is installed".
lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed; skipping lint"; \
	fi

# snapshot builds the entire release matrix — every platform, every
# archive, the checksum file — and uploads nothing. It is what makes a
# broken release pipeline a pull-request failure instead of a discovery
# made while cutting a release.
#
# IT IS NOT A PREREQUISITE OF ci, and that is a decision rather than an
# omission. It needs a tool this module does not build and cannot
# require, and it costs minutes rather than seconds. What ci DOES carry
# is the release tool's own validation of the configuration: the guard
# suite runs it when the tool is present and records a declared skip when
# it is not, so the check travels with the suite instead of needing a
# second place in this file that could disagree with it.
#
# The workflow that runs this on every change installs the tool at an
# exact pinned version and then runs this target, so the command CI types
# is the command a person types.
#
# --skip=sign, and it is measured rather than assumed. A snapshot skips
# announcing, publishing and validation; it does NOT skip signing, so
# without this the target fails at its last step on any machine with no
# signing tool — which is every machine, since a keyless signature needs
# an identity that exists only inside an approved release run. Asking for
# a signature here would mean either a check that cannot pass or an
# identity on a pull request from a stranger, and the second is worse.
# THE CHECK AFTER THE BUILD is the only thing that can see a
# member-count error or a misspelled archive name: the release tool's
# own validator reads the schema, and a format that cannot hold what it
# was given fails in the pipe rather than in the document. It asks the
# wrapper's own mapping for the six names it expects, so the release
# template and the install script's table are tied together instead of
# being two restatements of the same six names in different files.
snapshot:
	goreleaser release --snapshot --clean --skip=sign
	node scripts/check-release-assets.js dist

# surface-check reads the surfaces the test suite cannot: the messages,
# names and titles of a range being published. It is the same program the
# workflows run and the same one the pre-push hook runs, so the command
# that fails a push is a command a person can type.
#
# IT IS NOT A PREREQUISITE OF ci, and the reason is the shape of its
# subject rather than its cost. There is no push range in a working copy:
# a checkout is one state, and this check is about the difference between
# two. What ci DOES carry is the checker's own suite, the self-test
# included, so the instrument is tested by every run even though the
# measurement needs a range to point at.
#
# With no arguments it reads the workflow event it is running under. On a
# machine, pass the range: `-base <rev> -head <rev> -branch <name>`.
surface-check:
	go run ./tools/surfacecheck $(ARGS)

# hooks installs the pre-push hook, and it is OPT IN because a hook is
# not the gate and must never be mistaken for one. A hook lives in a
# directory git does not clone, is skipped by --no-verify, and is absent
# on CI; anything that has to hold has to hold in the workflow. What this
# buys is the failure arriving in a couple of seconds on the machine that
# wrote the message rather than a couple of minutes later in a run.
#
# What lands in the hooks directory is a SHIM that runs the tracked
# script, so the hook cannot go stale when that script changes — a copy
# would, silently, and the copy nobody re-installed is the one running
# when it matters.
#
# It refuses to overwrite a hook it did not write. Somebody else's
# pre-push hook is somebody else's work.
hooks:
	@dir="$$(git rev-parse --git-path hooks)"; \
	hook="$$dir/pre-push"; \
	mkdir -p "$$dir"; \
	if [ -e "$$hook" ] && ! grep -q 'scripts/pre-push' "$$hook"; then \
		echo "$$hook exists already and is not this shim, so it is somebody's"; \
		echo "own work. Move it aside and run make hooks again."; \
		exit 1; \
	fi; \
	printf '%s\n' \
		'#!/bin/sh' \
		'# Installed by `make hooks`. The hook is the tracked script; this' \
		'# shim only finds it, so it cannot go stale when that one changes.' \
		'exec sh "$$(git rev-parse --show-toplevel)/scripts/pre-push" "$$@"' \
		> "$$hook"; \
	chmod +x "$$hook"; \
	echo "installed $$hook"; \
	echo "it runs the same check the workflow does, over what you are about to push"

ci: fmt vet build test lint
