# Vigil Controller — Claude Code Instructions

## Project Context

Vigil is a Kubernetes controller that manages node readiness by watching for startup taints and removing them once all expected DaemonSet pods are Ready. It is an open-source project — all code, docs, and CI must be free of internal references.

## Code Quality Standards

- Target maximum reasonable test coverage
- Use `// coverage:ignore` only for genuinely untestable code (e.g., `os.Exit` paths)
- No internal Nextdoor references, URLs, domain names, or service names in code or docs
- Exception: the taint key `node.nextdoor.com/initializing` is the default config value

## Testing Strategy

- **Unit tests**: Table-driven tests using `testify`. Test alongside source in `*_test.go` files.
- **Integration tests**: Use `setup-envtest` for controller-runtime tests with a real etcd + API server.
- **E2E tests**: Ginkgo v2 + Gomega in `test/e2e/`, behind `//go:build e2e` tag. Run against Kind.
- Prefer real behavior over mocks. Use `fake.NewClientBuilder()` for unit tests, `setup-envtest` for integration.

## Development Workflow

- Use conventional commits with component scope: `feat(controller):`, `fix(reconciler):`, `test(e2e):`
- Open PRs in draft mode
- Pre-commit: `make lint` -> `go test -race ./...` -> commit

## Code Comments

- Comments explain **why**, not what
- READMEs are terse and information-dense
- Code comments are verbose where the logic is non-obvious

## Key Architecture Decisions

- Uses `k8s.io/component-helpers` for DaemonSet scheduling predicate evaluation
- Taint removal uses fresh API server reads (not informer cache) with optimistic concurrency
- Startup taint keys are a required configuration input (provisioner-agnostic)
- DaemonSet list is cached via informer; pod lookup uses `spec.nodeName` field indexer
