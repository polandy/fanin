GO_DIRS := $(wildcard cmd internal pkg)
GO_PKGS := $(patsubst %,./%/...,$(GO_DIRS))
COVERAGE_PKGS := internal/gate internal/sched internal/store internal/hook
COVERAGE_EXISTING := $(sort $(patsubst %/,%,$(dir $(wildcard $(addsuffix /*.go,$(COVERAGE_PKGS))))))

.PHONY: ci build vet test test-shuffle coverage integration fmt-check shell-check script-test check-no-sleeps \
	check-file-size docs-check private-check private-check-history scenario-check vendor-check lint vuln schema-check

# The local CI mirror: green before every push.
ci: fmt-check shell-check script-test vet check-no-sleeps check-file-size docs-check vendor-check build test test-shuffle \
	coverage lint vuln schema-check private-check private-check-history scenario-check

vendor-check:
	go mod tidy
	go mod vendor
	git diff --exit-code -- go.mod go.sum vendor
	@if [ -n "$$(git ls-files --others --exclude-standard -- vendor)" ]; then echo "vendor/ has untracked files: git add vendor"; exit 1; fi

lint:
	golangci-lint config verify
	golangci-lint run

vuln:
	govulncheck $(GO_PKGS)

build:
	go build -o bin/ ./cmd/...

vet:
	go vet $(GO_PKGS)

test:
	go test -race -count=1 $(GO_PKGS)

test-shuffle:
	go test -race -count=1 -shuffle=on -timeout=120s $(GO_PKGS)

# Profiles only the gated packages: cover mode fails on packages without tests (no covdata tool).
coverage:
	@if [ -z "$(COVERAGE_EXISTING)" ]; then echo "coverage: no gated package exists yet, skipped"; exit 0; fi; \
	go test -count=1 -coverprofile=cover.out $(patsubst %,./%/...,$(COVERAGE_EXISTING)) && \
	scripts/check-coverage.sh cover.out 90 $(COVERAGE_PKGS)

# Real-process tests; empty until the first adapter lands.
integration:
	go test -count=1 -tags integration $(GO_PKGS)

fmt-check:
	@out="$$(gofmt -l $(GO_DIRS))"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

shell-check:
	bash -n scripts/*.sh
	shellcheck scripts/*.sh

script-test:
	scripts/test-checks.sh

check-no-sleeps:
	scripts/check-no-sleeps.sh $(GO_DIRS)

check-file-size:
	scripts/check-file-size.sh 400 scripts/file-size-exceptions.txt $(GO_DIRS)

docs-check:
	scripts/check-docs-links.sh

private-check:
	scripts/check-private-data.sh

private-check-history:
	scripts/check-private-data.sh --history

# Every example config tree validates.
schema-check:
	go run ./internal/config/cmd/schemacheck config/examples

# Report mode until the tests exist; switch to --strict once every scenario has one.
scenario-check:
	scripts/check-scenarios.sh --report
