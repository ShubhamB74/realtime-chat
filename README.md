# Real-Time Chat & Messaging Platform

> Working title. The name is a placeholder; the technical slug is `realtime-chat` and the Kafka topic prefix is `chat.`, so a later rename touches only this file, the Go module path, and image names.

A distributed, real-time messaging platform with an asynchronous AI layer, built as an engineering project to demonstrate distributed systems, sync protocols, event-driven design, and production practices.

**Honest scoping note:** at the assumed scale (10k DAU, ~60 msg/s peak) a single service plus PostgreSQL would suffice in production. Kafka, Kubernetes, and multiple gateways are here to demonstrate and measure distributed patterns, not because load demands them. See ADR-001.

## Core design in one paragraph
Messages are committed to PostgreSQL with a per-conversation sequence number, then acknowledged. Online delivery uses a **fast path** (Redis pub/sub between gateways) that is best-effort. **Correctness comes from sync**: devices hold cursors and can always ask "what changed after seq N". Kafka, fed by a transactional outbox, drives only **derived pipelines** (notifications, media processing, AI). AI is async, opt-in, and has its own data store.

## Stack (provisional, see ADR-002)
React/TypeScript/Vite · Go · Python/FastAPI · PostgreSQL + pgvector · Redis (presence, registry, pub/sub) · Kafka (KRaft) · S3-compatible object storage (SeaweedFS in dev, see ADR-008) · Docker · Kubernetes · OpenTelemetry/Prometheus/Grafana · k6 · GitHub Actions. (gRPC removed from initial scope.)

## Repository layout
| Path | Purpose |
|---|---|
| `frontend/` | React web client |
| `backend/` | Go API, WebSocket gateway, workers |
| `ai-service/` | Python AI service (async only, own store) |
| `infrastructure/` | Docker, Kubernetes, observability config |
| `packages/` | Shared contracts: event schemas, OpenAPI |
| `scripts/` | Dev/CI helper scripts |
| `tests/` | Integration, e2e, load, chaos tests |
| `docs/` | PRD, HLD, LLD, ADRs, roadmap |

## Phase progress (revised, see docs/roadmap.md)
| Phase | Scope | Status |
|---|---|---|
| 0 | Requirements, architecture, repo foundation | Done (rev 2) |
| 1 | Infra + backend foundation | Done |
| 2 | Auth + conversations | **Code written, awaiting verification** |
| 3 | Thin vertical slice: WebSocket, persistence, seq, sync, k6 baseline | Not started |
| 4A | Fast-path delivery, cross-gateway routing, cursors, receipts, presence | Not started |
| 4B | Outbox, Kafka, workers, DLQ | Not started |
| 5 | Notifications, media pipeline, reliability | Not started |
| 6 | Search, multi-device completion, retention/deletion | Not started |
| 7 | AI layer | Not started |
| 8 | Kubernetes, load, chaos, CI/CD | Not started |
| 9 | Final review | Not started |

## Quick start
```
make bootstrap   # .env with generated secrets, Go/npm/pip dependencies and lock files
make up          # postgres, redis, object store
make migrate-up
make test && make test-integration
make run         # then in another shell: make check-health
```
Details and environment variables: [docs/deployment.md](docs/deployment.md). Run `make help` for all targets.

## Key documents
[PRD](docs/PRD.md) · [HLD](docs/HLD.md) · [LLD](docs/LLD.md) · [Roadmap](docs/roadmap.md) · [API](packages/openapi/openapi.yaml) · [Security](docs/security.md) · [ADRs](docs/adr/)
