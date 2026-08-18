GO ?= go
REPO_ROOT ?= ..
BASE_LEGACY ?= http://127.0.0.1:9118
BASE_GO ?= http://127.0.0.1:18118
OUT_DIR ?= dist

# Version metadata baked into the binary via -ldflags. Override from the
# release workflow, e.g. `make release VERSION=0.4.0`.
VERSION    ?= dev
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    ?= -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)

.PHONY: tidy build build-torrs run run-torrs build-fancdnregbrowser
.PHONY: release release-torrs release-all release-fancdnregbrowser clean-dist
.PHONY: compat-spec golden-capture golden-compare route-coverage
.PHONY: check fmt fmt-check vet test
.PHONY: monolith-stats sbom

tidy:
	$(GO) mod tidy

# ---------------------------------------------------------------------------
#  Quality gates — run `make check` before committing / in CI.
# ---------------------------------------------------------------------------

# fmt-check fails (nonzero) if any file is not gofmt-clean, printing the list.
fmt-check:
	@unformatted=$$(gofmt -l internal cmd); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt: the following files need formatting (run 'make fmt'):"; \
		echo "$$unformatted"; \
		exit 1; \
	fi

fmt:
	gofmt -w internal cmd

vet:
	$(GO) vet ./...

# Leaf/unit tests. Balancer integration tests that need network are env-gated
# and skipped by -short.
test:
	$(GO) test -short ./...

# One-shot gate for CI / pre-commit.
check: fmt-check vet test

# Track the internal/httpapi monolith debt (see internal/httpapi/ARCHITECTURE.md).
# Goal is DOWN over time. Add new features as their own internal/<feature>
# package + a thin handler, not as more flat files here.
monolith-stats:
	@echo "internal/httpapi debt (target: shrink — see internal/httpapi/ARCHITECTURE.md)"
	@files=$$(ls internal/httpapi/*.go | grep -v '_test\.go'); \
	 echo "  source files: $$(printf '%s\n' "$$files" | wc -l | tr -d ' ')"; \
	 echo "  LOC:          $$(cat $$files | wc -l | tr -d ' ')"; \
	 echo "  var globals:  $$(grep -hE '^var [a-zA-Z]' $$files | wc -l | tr -d ' ')"

# Software Bill of Materials — publish alongside each release so a closed-source
# binary's dependencies stay auditable (deps can be checked against CVE feeds).
sbom:
	@if command -v cyclonedx-gomod >/dev/null 2>&1 && \
	   cyclonedx-gomod app -json -licenses -main cmd/lampac-go -output sbom.cdx.json . 2>/dev/null; then \
		echo "SBOM → sbom.cdx.json (CycloneDX)"; \
	else \
		echo "CycloneDX unavailable (needs cyclonedx-gomod + a git repo) — plain module list instead."; \
		echo "  full CycloneDX: go install github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod@latest && run in the git repo"; \
		$(GO) list -m all > sbom.txt && \
		echo "SBOM → sbom.txt ($$($(GO) list -m all | wc -l | tr -d ' ') modules)"; \
	fi

build:
	$(GO) build ./...

build-torrs:
	$(GO) build -tags torrs ./...

run:
	$(GO) run ./cmd/lampac-go

run-torrs:
	$(GO) run -tags torrs ./cmd/lampac-go

build-fancdnregbrowser:
	$(GO) build -o fancdnregbrowser ./cmd/fancdnregbrowser/

# ---------------------------------------------------------------------------
#  Cross-compilation: plain lampac-go (without torrent server)
# ---------------------------------------------------------------------------

release: clean-dist
	@mkdir -p $(OUT_DIR)
	@for pair in "linux amd64" "linux arm64" "linux arm" "darwin amd64" "darwin arm64"; do \
		set -- $$pair; os=$$1; arch=$$2; \
		suffix="$$os-$$arch"; \
		if [ "$$arch" = "arm" ]; then \
			export GOARM=7; \
		fi; \
		echo "==> Building lampac-go-$$suffix"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 $(GO) build -trimpath \
			-ldflags="$(LDFLAGS)" \
			-o $(OUT_DIR)/lampac-go-$$suffix \
			./cmd/lampac-go || exit 1; \
		echo "    OK $(OUT_DIR)/lampac-go-$$suffix"; \
	done
	@echo "==> Release build complete (without torrent server)."

# ---------------------------------------------------------------------------
#  Cross-compilation: lampac-go-ts (with in-process torrent server)
# ---------------------------------------------------------------------------

release-torrs: clean-dist
	@mkdir -p $(OUT_DIR)
	@for pair in "linux amd64" "linux arm64" "linux arm" "darwin amd64" "darwin arm64"; do \
		set -- $$pair; os=$$1; arch=$$2; \
		suffix="$$os-$$arch"; \
		if [ "$$arch" = "arm" ]; then \
			export GOARM=7; \
		fi; \
		echo "==> Building lampac-go-ts-$$suffix (with torrent server)"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 $(GO) build \
			-tags torrs \
			-ldflags="$(LDFLAGS)" \
			-o $(OUT_DIR)/lampac-go-ts-$$suffix \
			./cmd/lampac-go || exit 1; \
		echo "    OK $(OUT_DIR)/lampac-go-ts-$$suffix"; \
	done
	@echo "==> Release build complete (with torrent server)."

# ---------------------------------------------------------------------------
#  Cross-compilation: fancdnregbrowser companion (chromedp+Xvfb registrar
#  used by [online.fancdn] use_browser_for_register=true). Linux-only —
#  needs xvfb + chrome on the target host.
# ---------------------------------------------------------------------------

release-fancdnregbrowser:
	@mkdir -p $(OUT_DIR)
	@for pair in "linux amd64" "linux arm64" "linux arm"; do \
		set -- $$pair; os=$$1; arch=$$2; \
		suffix="$$os-$$arch"; \
		if [ "$$arch" = "arm" ]; then \
			export GOARM=7; \
		fi; \
		echo "==> Building fancdnregbrowser-$$suffix"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 $(GO) build \
			-ldflags="-s -w" \
			-o $(OUT_DIR)/fancdnregbrowser-$$suffix \
			./cmd/fancdnregbrowser/ || exit 1; \
		echo "    OK $(OUT_DIR)/fancdnregbrowser-$$suffix"; \
	done

# ---------------------------------------------------------------------------
#  Build both variants for all platforms
# ---------------------------------------------------------------------------

release-all:
	@$(MAKE) release
	@$(MAKE) release-torrs
	@$(MAKE) release-fancdnregbrowser

clean-dist:
	@rm -rf $(OUT_DIR)

# ---------------------------------------------------------------------------
#  Compat / testing tools
# ---------------------------------------------------------------------------

compat-spec:
	$(GO) run ./tools/route_inventory -repo-root $(REPO_ROOT) -out compat/compat-spec.json

golden-capture:
	$(GO) run ./tools/golden_capture -base-url $(BASE_LEGACY) -input compat/routes-smoke.txt -out-dir compat/golden

golden-compare:
	$(GO) run ./tools/golden_compare -base-url $(BASE_GO) -input compat/routes-smoke.txt -golden-dir compat/golden

route-coverage:
	$(GO) run ./tools/route_coverage -spec compat/compat-spec.json -base-url $(BASE_GO)
