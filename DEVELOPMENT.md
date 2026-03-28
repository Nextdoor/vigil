# Development

## Prerequisites

- Go 1.24+
- Docker
- [Kind](https://kind.sigs.k8s.io/) (for E2E tests)
- [Helm](https://helm.sh/) 3.x
- [golangci-lint](https://golangci-lint.run/) v2.11+

## Setup

```bash
git clone https://github.com/Nextdoor/vigil-controller.git
cd vigil-controller
go mod download
```

## Running Locally

```bash
# Copy the example config
cp config.example.yaml config.local.yaml

# Run with a local kubeconfig
make run
```

## Building

```bash
# Build binary
make build

# Build Docker image
make docker-build
```

## Testing

```bash
# Run unit tests
make test

# View coverage
make cover

# Run E2E tests (requires Kind cluster)
make kind-create
make test-e2e
make kind-delete
```

## Pre-Commit Checklist

Before committing:

1. `make lint` — passes with no warnings
2. `go test -race ./...` — all tests pass
3. `go mod tidy` — go.mod/go.sum are clean

## Conventional Commits

All commits must use conventional commit format:

```
feat(controller): add daemonset discovery
fix(reconciler): handle nil node taints
chore(deps): update controller-runtime
test(e2e): add timeout removal test
docs(website): add configuration reference
```
