# Image URL to use for docker-build
IMG ?= ghcr.io/petersr/spiffile-operator:dev

.PHONY: all
all: build

.PHONY: build
build: ## Build the operator binary into bin/.
	CGO_ENABLED=0 go build -o bin/spiffile-operator .

.PHONY: test
test: ## Run the Go tests.
	go test ./...

.PHONY: fmt
fmt: ## gofmt the tree.
	gofmt -w .

.PHONY: vet
vet: ## go vet the tree.
	go vet ./...

.PHONY: lint
lint: vet ## gofmt check + go vet, plus golangci-lint when installed.
	test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	@if command -v golangci-lint >/dev/null; then golangci-lint run; \
	else echo "golangci-lint not installed, skipping (CI runs it)"; fi

.PHONY: docker-build
docker-build: ## Build the operator image.
	docker build -t $(IMG) .

.PHONY: helm-lint
helm-lint: ## Lint and render the Helm chart.
	helm lint charts/spiffile-operator
	helm template test charts/spiffile-operator --set webhook.enabled=true >/dev/null

.PHONY: verify-crds
verify-crds: ## Ensure deploy/crd.yaml and the chart CRDs are identical.
	diff -u deploy/crd.yaml charts/spiffile-operator/crds/crds.yaml

.PHONY: help
help: ## Show this help.
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-14s %s\n", $$1, $$2}'
