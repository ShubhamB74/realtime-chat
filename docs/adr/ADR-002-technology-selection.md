# ADR-002: Technology selection

Status: Proposed (revision 2)
Date: 2026-11-13

## Decision
Go (gateway/backend), Python/FastAPI (AI), PostgreSQL + pgvector, Redis, Kafka, MinIO, WebSockets, REST. React/TypeScript frontend.

Changes in revision 2:
- **gRPC removed** from initial scope. No concrete need exists: worker-to-gateway delivery uses Redis pub/sub (ADR-006) and the AI service uses an internal REST API. Reintroduce only with an ADR.
- **Kafka in KRaft mode** (no ZooKeeper). Confirm version support in Phase 1. Redpanda (Kafka API compatible, lighter) is a candidate for local development only; evaluate in Phase 1 and record the result here. Production-style environments use Apache Kafka.
- **Docker Compose profiles:** `core` (PostgreSQL, Redis, MinIO) runs through Phase 4A; `streaming` (Kafka, UI) is added for Phase 4B onward.

## Version policy
No versions chosen in Phase 0. In Phase 1 verify current stable releases and compatibility from current sources, pin in lockfiles, and record them here. Do not take versions from memory.

## Consequences
+ Mature components, lighter local development.
- Kafka and Kubernetes are heavy for the scale; justified by project goals only.
- pgvector in a separate database/schema (ADR-004), so vector load does not contend with the OLTP schema.

## Search
PostgreSQL full-text search first; OpenSearch only if measurements justify it.
