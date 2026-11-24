# cactusTrade — `make check` is the quality bar (CLAUDE.md §5). Every target that needs Go
# or Node has a *-in-container twin that needs only Docker (or Podman: DOCKER=podman).
SHELL := /usr/bin/env bash
.SHELLFLAGS := -euo pipefail -c

GO_PKGS   := ./...
COVER_MIN := 80
VERSION   := $(shell tr -d '[:space:]' < VERSION 2>/dev/null || echo dev)
BUILD     := $(shell git rev-list --count HEAD 2>/dev/null || echo 0)
LDFLAGS   := -s -w -X main.version=$(VERSION) -X main.build=$(BUILD)

DOCKER     ?= docker
GO_IMAGE   ?= golang:1.26.11-bookworm@sha256:4ebd099cae89439fff17af404a9c724079d439b4d92178819318735312ee9eb3
LINT_IMAGE ?= golangci/golangci-lint:v2.14.0@sha256:ad862ba6b3798cbe0fd9fd7408d498fd74fbd2623a92406b2fd3898faf0bf98f
IN_GO       = $(DOCKER) run --rm -v "$(CURDIR)":/src -w /src -v cactustrade-gomod:/go/pkg/mod -v cactustrade-gobuild:/root/.cache/go-build $(GO_IMAGE)
CONTRACTS_TMP := .cache/contracts
AUDIT_TMP     := .cache/audit

.PHONY: help gen fmt fmt-check lint vet test cover test-int vuln audit contracts web-install web-check build run \
        image check etoro-spec install uninstall status backup dist \
        lint-in-container test-in-container build-in-container check-in-container

help: ## list targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-22s %s\n", $$1, $$2}'

gen: web-install ## regenerate the OpenAPI server and the web API types
	oapi-codegen -config api/openapi/oapi-codegen.yaml api/openapi/cactustrade.yaml
	pnpm -C web gen:api

fmt: ## format Go code
	gofumpt -w cmd internal tools web api migrations

fmt-check:
	@out="$$(gofumpt -l cmd internal tools web api migrations | grep -v '\.gen\.go$$' || true)"; \
	  if [[ -n "$$out" ]]; then echo "gofumpt needed:"; echo "$$out"; exit 1; fi

lint: ## golangci-lint (incl. depguard layering rules)
	golangci-lint run $(GO_PKGS)

vet:
	go vet $(GO_PKGS)

test: ## unit tests (incl. the SQLite repository suite and the eToro allowlist guardrail)
	go test -race -count=1 $(GO_PKGS)

cover: ## unit tests with coverage gates on domain and app
	go test -race -count=1 -coverprofile=coverage.out $(GO_PKGS) >/dev/null
	@for p in internal/domain internal/app; do \
	  pct=$$(go test -count=1 -cover ./$$p | sed -E 's/.*coverage: ([0-9.]+)%.*/\1/'); \
	  echo "$$p coverage $$pct%"; \
	  awk -v p="$$pct" -v m=$(COVER_MIN) 'BEGIN{exit !(p>=m)}' || { echo "$$p below $(COVER_MIN)%"; exit 1; }; \
	done

test-int: ## MariaDB 11 integration tests (Docker; testcontainers)
	go test -race -count=1 -tags integration -timeout 15m ./internal/adapters/sqlstore/

vuln: ## known-vulnerability scan
	go run golang.org/x/vuln/cmd/govulncheck@latest $(GO_PKGS)

audit: vuln web-install ## dependency audit (PLAN 7.7): go mod verify, govulncheck, licences, pnpm audit, image digests
	go mod verify
	@mkdir -p $(AUDIT_TMP)
	go list -m -json all >$(AUDIT_TMP)/modules.json
	go list -deps -test -tags integration -f '{{with .Module}}{{.Path}}{{end}}' $(GO_PKGS) | sort -u >$(AUDIT_TMP)/build.txt
	pnpm -C web licenses list --prod --json >$(AUDIT_TMP)/pnpm.json
	go run ./tools/licencecheck -go-modules $(AUDIT_TMP)/modules.json -go-build $(AUDIT_TMP)/build.txt -pnpm $(AUDIT_TMP)/pnpm.json
	pnpm -C web audit --prod --audit-level=moderate
	go test -count=1 -run 'TestImagesPinnedByDigest' ./cmd/cactustrade

contracts: web-install ## generated code must match the OpenAPI contract
	@mkdir -p $(CONTRACTS_TMP)
	sed "s#^output:.*#output: $(CONTRACTS_TMP)/api.gen.go#" api/openapi/oapi-codegen.yaml >$(CONTRACTS_TMP)/cfg.yaml
	oapi-codegen -config $(CONTRACTS_TMP)/cfg.yaml api/openapi/cactustrade.yaml
	diff -q $(CONTRACTS_TMP)/api.gen.go internal/adapters/http/api.gen.go
	cd web && pnpm -s exec openapi-typescript ../api/openapi/cactustrade.yaml -o ../$(CONTRACTS_TMP)/schema.d.ts >/dev/null
	diff -q $(CONTRACTS_TMP)/schema.d.ts web/src/lib/schema.d.ts
	@echo "contracts in sync"

web-install:
	@[[ -d web/node_modules ]] || pnpm -C web install --frozen-lockfile

web-check: web-install ## web lint, typecheck, tests, build
	pnpm -C web check

build: web-check ## bin/cactustrade with the web app embedded
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/cactustrade ./cmd/cactustrade

# ---- releases (PLAN 7.9) -------------------------------------------------------------------
# dist/ holds one release: a binary per OS and CPU (the web app embedded), SHA256SUMS and its
# signature (OD31: `ssh-keygen -Y sign`, namespace cactustrade-release), checked at once the
# way setup.sh checks it. The private key never enters the repository; RELEASE_SIGNERS holds
# its public half. Not part of `make check`.
DIST_DIR        := dist
DIST_TARGETS    := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64
RELEASE_KEY     ?= $(HOME)/.config/cactustrade/release_ed25519
RELEASE_SIGNERS ?= scripts/allowed_signers

dist: web-check ## release binaries (linux/darwin/windows × amd64/arm64), SHA256SUMS and its signature in dist/
	rm -rf $(DIST_DIR) && mkdir -p $(DIST_DIR)
	for t in $(DIST_TARGETS); do \
	  os=$${t%/*}; arch=$${t#*/}; ext=; [[ $$os == windows ]] && ext=.exe; \
	  echo "$(DIST_DIR)/cactustrade-$$os-$$arch$$ext"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" \
	    -o "$(DIST_DIR)/cactustrade-$$os-$$arch$$ext" ./cmd/cactustrade; \
	done
	scripts/release-sums.sh $(DIST_DIR)
	scripts/release-sign.sh "$(RELEASE_KEY)" $(DIST_DIR)
	scripts/release-verify.sh $(DIST_DIR) "$(RELEASE_SIGNERS)"
	cat $(DIST_DIR)/SHA256SUMS

run: build ## standalone: SQLite in ./data, self-signed TLS, https://localhost:8443
	./bin/cactustrade serve

image: ## the container image (built entirely in containers)
	$(DOCKER) build --build-arg VERSION=$(VERSION) --build-arg BUILD=$(BUILD) -t cactustrade:latest .

etoro-spec: ## download eToro's OpenAPI spec; report allowlisted paths it no longer has
	@mkdir -p .cache/etoro
	curl -fsSL -A "Mozilla/5.0 (cactustrade docs check)" -o .cache/etoro/openapi.json https://api-portal.etoro.com/api-reference/openapi.json
	@python3 -I -c 'import json,sys; d=json.load(open(sys.argv[1])); print("eToro OpenAPI", d["info"]["version"]); [print(m.upper(), p) for p,o in sorted(d["paths"].items()) for m in o if m in ("get","post","put","patch","delete")]' .cache/etoro/openapi.json > .cache/etoro/paths.txt
	@head -1 .cache/etoro/paths.txt; echo "allowlisted paths missing from the spec (should be none):"; \
	  grep -oE '"/api/[^"]+"' internal/adapters/etoro/allowlist.go | tr -d '"' | sort -u | while read -r p; do grep -q " $$p$$" .cache/etoro/paths.txt || echo "  $$p"; done

check: fmt-check lint vet cover test-int vuln audit contracts web-check ## THE quality bar

# ---- container-only variants (need nothing but Docker/Podman) ---------------------------
lint-in-container: ## golangci-lint in its image
	$(DOCKER) run --rm -v "$(CURDIR)":/src -w /src $(LINT_IMAGE) golangci-lint run $(GO_PKGS)

test-in-container: ## unit tests in the Go image
	$(IN_GO) go test -race -count=1 $(GO_PKGS)

build-in-container: ## bin/cactustrade-<os>-<arch> built in containers (GOOS/GOARCH; default linux/amd64)
	$(DOCKER) build --target export --build-arg VERSION=$(VERSION) --build-arg BUILD=$(BUILD) \
	  --build-arg TARGETOS=$${GOOS:-linux} --build-arg TARGETARCH=$${GOARCH:-amd64} --output type=local,dest=bin .
	mv bin/cactustrade bin/cactustrade-$${GOOS:-linux}-$${GOARCH:-amd64}$$( [[ $${GOOS:-linux} == windows ]] && echo .exe || true )

check-in-container: lint-in-container test-in-container image ## the container-only quality gates

# ---- deployment shortcuts ----------------------------------------------------------------
install: ## deploy on Docker Desktop (app + MariaDB)
	./deploy.sh --install --dockerdesktop
uninstall: ## remove from Docker Desktop, keeping data
	./deploy.sh --uninstall --dockerdesktop
status:
	./deploy.sh --status --dockerdesktop
backup: ## dump the database now
	./deploy.sh --backup-now --dockerdesktop
