BINARY   := claude-auto-permission
BUILDDIR := build
MAIN_PKG := ./cmd/$(BINARY)

.PHONY: build clean test install install-hook proto generate e2e e2e-full

build: $(BUILDDIR)/$(BINARY)

$(BUILDDIR)/$(BINARY): go.mod go.sum $(wildcard **/*.go)
	go build -o $@ $(MAIN_PKG)

clean:
	rm -rf $(BUILDDIR)

test:
	go test ./...

install:
	go install $(MAIN_PKG)

install-hook: install
	@./scripts/install-hook.sh

proto:
	buf generate

# Regenerate proto bindings and gomock test doubles.
gen: proto
	go generate ./...

# E2E conformance suite. Classifier cases hit real Bedrock.
# Quick-tagged subset by default; `make e2e-full` runs everything.
e2e:
	CLAUDE_AUTO_PERMISSION_E2E=1 go test -timeout=300s ./test/e2e/...

e2e-full:
	CLAUDE_AUTO_PERMISSION_E2E=1 CLAUDE_AUTO_PERMISSION_E2E_FULL=1 go test -timeout=600s ./test/e2e/...
