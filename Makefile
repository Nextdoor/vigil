# Vigil Controller Makefile

# Go parameters
BINARY_NAME=manager
BINARY_DIR=bin
GO=go
GOFLAGS=-trimpath
LDFLAGS=-s -w

# Helm parameters
CHART_DIR=charts/vigil-controller

# E2E parameters
KIND_CLUSTER_NAME=vigil-test
KIND_IMAGE=kindest/node:v1.31.0

.PHONY: all
all: build

##@ General

.PHONY: help
help: ## Display this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: fmt
fmt: ## Run go fmt
	$(GO) fmt ./...

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: lint
lint: ## Run golangci-lint
	golangci-lint run

.PHONY: run
run: ## Run controller locally with config.local.yaml
	$(GO) run ./cmd/main.go --config=config.local.yaml

##@ Build

.PHONY: build
build: fmt vet ## Build the binary
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY_DIR)/$(BINARY_NAME) ./cmd/main.go

.PHONY: docker-build
docker-build: ## Build Docker image
	docker build -t vigil-controller:latest .

.PHONY: docker-push
docker-push: ## Push Docker image
	docker push vigil-controller:latest

##@ Testing

.PHONY: test
test: ## Run unit tests with coverage
	KUBEBUILDER_ASSETS="$(shell go run sigs.k8s.io/controller-runtime/tools/setup-envtest@latest use -p path 2>/dev/null || echo '')" \
		$(GO) test $$($(GO) list ./... | grep -v -e /e2e -e /test/) -coverprofile cover.out -covermode=atomic -race

.PHONY: test-e2e
test-e2e: ## Run E2E tests (requires Kind cluster)
	$(GO) test -tags=e2e -v -timeout=20m -count=1 ./test/e2e/...

.PHONY: test-stress
test-stress: ## Run stress tests (30min, 10k nodes, envtest)
	KUBEBUILDER_ASSETS="$(shell go run sigs.k8s.io/controller-runtime/tools/setup-envtest@latest use -p path 2>/dev/null || echo '')" \
		$(GO) test -tags=stress -v -timeout=35m -count=1 ./test/stress/...

.PHONY: cover
cover: ## Display coverage report
	$(GO) tool cover -func cover.out

.PHONY: coverhtml
coverhtml: ## Generate HTML coverage report
	$(GO) tool cover -html cover.out -o cover.html

##@ Kind Cluster

.PHONY: kind-create
kind-create: ## Create Kind cluster for development
	kind create cluster --name $(KIND_CLUSTER_NAME) --image $(KIND_IMAGE)

.PHONY: kind-delete
kind-delete: ## Delete Kind cluster
	kind delete cluster --name $(KIND_CLUSTER_NAME)

.PHONY: kind-load
kind-load: docker-build ## Load Docker image into Kind
	kind load docker-image vigil-controller:latest --name $(KIND_CLUSTER_NAME)

##@ Helm

.PHONY: helm-docs
helm-docs: ## Generate Helm chart documentation
	cd $(CHART_DIR) && helm-docs

.PHONY: helm-docs-check
helm-docs-check: helm-docs ## Verify Helm docs are up to date
	@git diff --exit-code $(CHART_DIR)/README.md || (echo "Helm docs are out of date. Run 'make helm-docs' and commit the changes." && exit 1)

##@ Cleanup

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BINARY_DIR) cover.out cover.html
