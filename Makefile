BINARY   := claude-auto-permission
BUILDDIR := build
MAIN_PKG := ./cmd/$(BINARY)

.PHONY: build clean test install install-hook proto generate e2e

build: $(BUILDDIR)/$(BINARY)

$(BUILDDIR)/$(BINARY):
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
	buf lint
	buf generate

# Regenerate proto bindings and gomock test doubles.
gen: proto
	go generate ./...

# End-to-end suites, run against the real compiled hook over its stdin/stdout wire protocol:
# smoke (hermetic), bash (deterministic static-rule conformance), and classifier (model evals).
# The classifier evals hit real Bedrock — they cost money and need valid AWS credentials.
e2e:
	CLAUDE_AUTO_PERMISSION_E2E=1 go test -timeout=600s ./test/e2e/...
