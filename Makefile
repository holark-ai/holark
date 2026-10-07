.PHONY: dev build check-go frontend-build clean test test-go test-web test-integration test-opencode test-claude test-all install-test-browser install-test-browser-firefox test-browser pr-scenario-happy-path pr-scenario-outdated-description pr-scenario-outdated-review pr-scenario-rebase-click-conflict pr-scenario-regeneration-failure pr-scenario-publication-failure pr-scenario-description-save-failure lint terminal-worker-deps

PKG ?= ./...
WEB_TEST_ARGS ?=
BROWSER_TEST_ARGS ?=
PR_SCENARIO_BROWSER_ARGS ?= --open-browser
GO_TEST_ARGS ?=
NPM_VERSION ?=

web/node_modules/.package-lock.json: web/package.json web/package-lock.json
	npm --prefix web ci

terminal-worker-deps: terminal-worker/node_modules/.package-lock.json

terminal-worker/node_modules/.package-lock.json: terminal-worker/package.json terminal-worker/package-lock.json
	npm --prefix terminal-worker ci

frontend-build: web/node_modules/.package-lock.json
	npm --prefix web run build

check-go:
	@command -v go >/dev/null 2>&1 || { \
		printf '%s\n' 'Go is required to build Holark but was not found on PATH.' \
			'Install Go 1.24 or newer using your platform package manager or the official Go installer, then rerun make.' \
			'If Go is already installed, add its bin directory to PATH.' >&2; \
		exit 1; \
	}

dev: check-go frontend-build terminal-worker-deps
	go run ./cmd/holark

build: check-go frontend-build terminal-worker-deps
	go build -o bin/holark ./cmd/holark

.PHONY: package-npm
package-npm: check-go frontend-build terminal-worker-deps
	@for target in linux-amd64 linux-arm64 darwin-amd64 darwin-arm64; do \
		printf 'Building Holark for %s\n' "$$target"; \
		GOOS=$${target%-*} GOARCH=$${target#*-} CGO_ENABLED=0 \
			go build -o "bin/npm/$$target/holark" ./cmd/holark || exit $$?; \
	done
	node packaging/npm/pack.mjs "$(NPM_VERSION)"

clean:
	rm -rf bin internal/localshell/frontend/dist web/node_modules terminal-worker/node_modules

test-go: check-go frontend-build terminal-worker-deps
	go test $(GO_TEST_ARGS) $(PKG)

test: test-go test-web

test-web: web/node_modules/.package-lock.json
	npm --prefix web test -- $(WEB_TEST_ARGS)

test-integration: check-go frontend-build terminal-worker-deps
	go test -tags=integration $(GO_TEST_ARGS) $(PKG)

# Real CLI tests make model requests and are excluded from make test.
test-opencode: check-go frontend-build terminal-worker-deps
	HOLARK_OPENCODE_E2E=1 go test -tags=opencode_e2e ./internal/harness/opencode ./internal/localapp -run '^TestOpenCodeE2E' -count=1 -timeout=8m -v $(GO_TEST_ARGS)

test-claude: build
	HOLARK_CLAUDE_E2E=1 CLAUDE_E2E_HOOK_EXECUTABLE='$(CURDIR)/bin/holark' go test -tags=claude_e2e ./internal/localapp -run '^TestClaudeCodeE2E' -count=1 -timeout=8m -v $(GO_TEST_ARGS)

test-all:
	$(MAKE) test
	$(MAKE) test-integration
	$(MAKE) test-browser

install-test-browser: web/node_modules/.package-lock.json
	npm --prefix web exec -- playwright install chromium

install-test-browser-firefox: web/node_modules/.package-lock.json
	npm --prefix web exec -- playwright install firefox

test-browser: web/node_modules/.package-lock.json
	npm --prefix web run test:e2e -- --project=chromium $(BROWSER_TEST_ARGS)

test-browser-firefox: web/node_modules/.package-lock.json
	npm --prefix web run test:e2e -- --project=firefox

# Browser scenario usage: internal/browsere2e/fixture/SCENARIOS.md
pr-scenario-happy-path: PR_SCENARIO_FLOW := happy-path
pr-scenario-outdated-description: PR_SCENARIO_FLOW := outdated-description
pr-scenario-outdated-review: PR_SCENARIO_FLOW := outdated-review
pr-scenario-rebase-click-conflict: PR_SCENARIO_FLOW := rebase-click-conflict
pr-scenario-regeneration-failure: PR_SCENARIO_FLOW := regeneration-failure
pr-scenario-publication-failure: PR_SCENARIO_FLOW := publication-failure
pr-scenario-description-save-failure: PR_SCENARIO_FLOW := description-save-failure

pr-scenario-happy-path pr-scenario-outdated-description pr-scenario-outdated-review pr-scenario-rebase-click-conflict pr-scenario-regeneration-failure pr-scenario-publication-failure pr-scenario-description-save-failure: _run-pr-scenario

_run-pr-scenario: frontend-build
	go run ./internal/browsere2e/fixture --listen 127.0.0.1:0 --static-dir internal/localshell/frontend/dist --scenario pull-request-creation --pull-request-flow $(PR_SCENARIO_FLOW) $(PR_SCENARIO_BROWSER_ARGS)

lint: check-go web/node_modules/.package-lock.json
	go vet ./...
	npm --prefix web run lint
	npm --prefix web run typecheck

.PHONY: pr-scenario-lifecycle
.PHONY: harness-scenario
harness-scenario: check-go frontend-build terminal-worker-deps
	go run ./internal/browsere2e/fixture --listen 127.0.0.1:0 --static-dir internal/localshell/frontend/dist --scenario harness-versions $(PR_SCENARIO_BROWSER_ARGS)

pr-scenario-lifecycle: PR_SCENARIO_FLOW := action-priority
pr-scenario-lifecycle: _run-pr-scenario
