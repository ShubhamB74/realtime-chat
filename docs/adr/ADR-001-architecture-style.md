# ADR-001: Architecture style

Status: Proposed (revision 2)
Date: 2026-11-13

## Decision
Modular, event-driven architecture with few deployables, not fine-grained microservices.

- One Go codebase producing role-specific binaries: `api`, `gateway`, `worker`. Domain logic is infrastructure-independent so roles can split later.
- Separate Python `ai-service` (different language, scaling profile, failure modes), isolated from the delivery path, with its own data store.
- PostgreSQL is the source of truth. Redis is a non-authoritative registry, presence store, and pub/sub bus.
- **Kafka drives derived pipelines only** (notifications, media, AI, analytics). It is not in the online delivery path (ADR-005).
- Sync is the correctness mechanism; push is an optimization.

## Context
Capacity estimates (~60 msg/s peak) do not require microservices or Kafka. Real systems at this scale would use one service with PostgreSQL. This project deliberately demonstrates distributed patterns and will measure them; that is the justification, and the README says so.

## Consequences
+ Fewer moving parts, lower delivery latency, simpler failure analysis.
+ AI failure is isolated.
- Gateway and worker roles share a codebase and need package discipline.
- Two mechanisms (fast path and sync) must stay consistent; mitigated by one idempotent client reducer.

## Alternatives rejected
Full microservices (overhead, no scale justification); Kafka in the delivery path (extra hops, outbox poll latency, no correctness gain once sync exists).
