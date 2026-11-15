# ADR-002: Technology selection

Status: Proposed (revision 2)
Date: 2026-11-13

## Decision
Go (gateway/backend), Python/FastAPI (AI), PostgreSQL + pgvector, Redis, Kafka, S3-compatible object storage (MinIO replaced, see ADR-008), WebSockets, REST. React/TypeScript frontend.

Changes in revision 2:
- **gRPC removed** from initial scope. No concrete need exists: worker-to-gateway delivery uses Redis pub/sub (ADR-006) and the AI service uses an internal REST API. Reintroduce only with an ADR.
- **Kafka in KRaft mode** (no ZooKeeper). Confirm version support in Phase 1. Redpanda (Kafka API compatible, lighter) is a candidate for local development only; evaluate in Phase 1 and record the result here. Production-style environments use Apache Kafka.
- **Docker Compose profiles:** `core` (PostgreSQL, Redis, object store) runs through Phase 4A; `streaming` (Kafka, UI) is added for Phase 4B onward.

## Version policy
No versions chosen in Phase 0. In Phase 1 verify current stable releases and compatibility from current sources, pin in lockfiles, and record them here. Do not take versions from memory.

## Consequences
+ Mature components, lighter local development.
- Kafka and Kubernetes are heavy for the scale; justified by project goals only.
- pgvector in a separate database/schema (ADR-004), so vector load does not contend with the OLTP schema.

## Search
PostgreSQL full-text search first; OpenSearch only if measurements justify it.

## Phase 1 version resolution (2026-11-13)
Sources are web searches; secondary sources are marked. Anything not confirmed on the vendor site must be re-verified before first real use.

| Component | Choice | Evidence | Status |
|---|---|---|---|
| PostgreSQL | 18 (image `postgres:18`) | 18.7 released 2026-11-12 per postgresql.org announcement; PostgreSQL 19 was at RC2, so not used | Verified (primary source) |
| Redis | 8.6 line (`redis:8.6`) | Redis 8.6.x release notes on redis.io (8.6.6 dated Aug 2026; a newer patch may exist) | Verified line, patch not confirmed |
| Kafka | `apache/kafka:4.3.1`, KRaft | Third-party docs list 4.3.1 as current; another source mentions 4.4.0 | **Conflicting; verify at kafka.apache.org/downloads** |
| Go | 1.27 line (`go.mod` minimum 1.26) | Go 1.27 released 2026-08-19 (go.dev blog source); a secondary source reports 1.27.4 current | Verify patch at go.dev/dl |
| Object store | SeaweedFS (dev) | ADR-008 | Image tag **not pinned**, see TODO in compose |
| Kafka UI | `kafbat/kafka-ui` | Maintained fork name from memory | **Unverified, not pinned** |
| Node | 22 | Present in the development sandbox | Verified locally |
| Python | 3.12 | Present in the development sandbox | Verified locally |
| Go modules, npm and pip packages | Resolved by tooling at `make deps`, `make frontend-deps`, `make ai-deps` | Lock files (`go.sum`, `package-lock.json`, `requirements*.lock`) are the pin | **Not yet resolved**: no network in the authoring environment |

Pinned versions must be written back to this table after the first successful `make bootstrap`.
