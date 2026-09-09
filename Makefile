# make ci is the test gate: every check that can fail a change runs
# inside it, so what passes locally and what passes in CI cannot diverge.
#
# TWO TARGETS SIT OUTSIDE IT and each says why where it is defined. The
# short of it: one needs a tool this module cannot require, and the other
# needs a push range, which a working copy does not have.

export CGO_ENABLED := 0

.PHONY: build test vet fmt lint snapshot surface-check hooks ci

build:
	go build -trimpath ./...
	go build -trimpath -o bin/curious ./cmd/curious

# test grows as suites arrive that go test cannot see on its own (a
# release-build check, a wrapper package's own test runner) — each lands
# here as an additional prerequisite so one command still sees the whole
# repository.
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
test:
	go run ./tools/skipcheck -- -count=1 ./...

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
snapshot:
	goreleaser release --snapshot --clean --skip=sign

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
