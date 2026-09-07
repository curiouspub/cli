# make ci is the single entry point: CI runs this target and nothing
# else, so local and CI cannot diverge.

export CGO_ENABLED := 0

.PHONY: build test vet fmt lint ci

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

ci: fmt vet build test lint
