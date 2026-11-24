GO_PKGS := ./cmd/... ./internal/...

.PHONY: ci build vet test fmt-check private-check private-check-history

# The local CI mirror: green before every push.
ci: fmt-check vet build test private-check private-check-history

build:
	go build -o bin/ $(GO_PKGS)

vet:
	go vet $(GO_PKGS)

test:
	go test -race -count=1 $(GO_PKGS)

fmt-check:
	@out="$$(gofmt -l cmd internal)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

private-check:
	scripts/check-private-data.sh

private-check-history:
	scripts/check-private-data.sh --history
