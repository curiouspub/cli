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
test:
	go test ./...

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
