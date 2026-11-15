# Phase 1 Acceptance Report

Date: 2026-11-13. Status: **code written, NOT yet accepted.** Acceptance requires you to run the commands below on a machine with Go, Docker and network access and report the results.

## Authoring environment limits
Python 3.12 and Node 22 were present. **Go, Docker, PostgreSQL, Redis and network access were not.** Therefore no Go code has been compiled, formatted, linted or tested, and no container has been started.

## What was actually executed
| Check | Result |
|---|---|
| `docker-compose.yml` and prod template parse as YAML; profiles and healthchecks present as intended | Passed |
| CI workflow, dependabot and golangci config parse as YAML | Passed |
| `package.json` and `tsconfig.json` parse as JSON | Passed |
| Python files compile (`py_compile`) | Passed |
| Migration files have exactly one Up and Down and balanced statement markers | Passed |
| `scripts/init-env.sh` generates `.env` with random secrets, mode 600, and does not overwrite | Passed |
| `make -n up`, `make help`, `make docs-check` | Passed |
| Version lookups for PostgreSQL, Redis, Kafka, Go, MinIO status | Done via web search; see ADR-002 for confidence levels |

## Written but NOT executed
Go build, `gofmt`, `go vet`, golangci-lint, all Go unit tests (config, logging, retry, httpx, health), the PostgreSQL integration tests, goose migrations, `docker compose up`, container health checks, `npm install/build`, `pytest`, `ruff`, `mypy`, and the CI pipeline.

Expect possible first-run friction: `gofmt` whitespace differences (run `make fmt`), a compile error or two, image tags that need adjusting (Kafka UI, SeaweedFS, `apache/kafka:4.3.1`, `postgres:18` volume path), and action tags in CI.

## Verification procedure (please run and send me the output of any failure)
```
make bootstrap
make fmt
make up                 # expect all core services healthy
make migrate-up         # expect 3 migrations applied
make migrate-down && make migrate-up   # roll back one and re-apply (repeat for full down)
make test
make test-integration
make lint
make run                # separate shell: make check-health  (expect /readyz 200 with postgres and redis up)
# stop the backend with Ctrl+C: expect "shutdown complete" in logs
make up-streaming       # then re-run check-health: kafka should report up
make up-apps            # backend, frontend (http://127.0.0.1:5173) and ai-service healthy
make ai-test && make frontend-build
```

## Requirements status
| Phase 1 requirement | Status |
|---|---|
| Go modules, Python venv/deps, TS project, env config, gitignore, Makefile, dev scripts | Written |
| Compose: PostgreSQL, Redis, Kafka, Kafka UI, object store, backend, frontend, AI service, volumes, health checks, dev and prod templates | Written (object store is SeaweedFS, not MinIO: ADR-008) |
| Go: entry point, config, structured logging, HTTP server, middleware, DB and Redis connections, health, readiness, graceful shutdown, error handling | Written |
| Migration infrastructure and `users`, `user_sessions`, `devices` with UUIDs and constraints | Written |
| Unit tests, DB integration tests, health checks, lint, format, basic CI | Written |
| "Developer can clone and start the environment using documented commands" | **Unverified** |

## Known limitations
- `go.sum`, `package-lock.json` and Python lock files do not exist yet; `make bootstrap` creates them and you must commit them. CI and Docker builds fail until then.
- Kafka and object store connectivity is only a TCP readiness probe (optional checks); there are no Kafka or S3 client libraries yet.
- No Redis integration test.
- Unknown routes return the JSON error contract, but wrong-method requests on known routes get the standard library's plain-text 405.
- Frontend uses Tailwind via its Vite plugin; shadcn/ui is not initialized (its CLI is interactive: run it after `make frontend-deps`).
- `uuidv7()` ties the schema to PostgreSQL 18.
- Two compose images are unpinned (marked TODO); dev object store has no S3 credentials.
- ADR-008 (replacing MinIO) is a proposal that needs your confirmation.
