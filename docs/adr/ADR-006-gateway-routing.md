# ADR-006: Gateway routing mechanism

Status: Proposed. Implemented in Phase 4A with the refinements recorded in ADR-011 (registry shares the presence structure; broadcast channel for presence; instant session disconnect over the bus was not built)
Date: 2026-11-13

## Decision
- **Registry (Redis):** `conn:{user_id}` maps to connection entries `{conn_id, gateway_id, device_id}` with TTL refreshed by heartbeats. `gateway:{id}` heartbeats let workers discard entries of dead gateways.
- **Bus (Redis pub/sub):** each gateway subscribes to its own channel `gw:{gateway_id}`. To deliver, the sender's gateway does one pipelined registry lookup for recipients, groups connections by gateway, and publishes once per target gateway. Same-gateway connections are delivered locally without Redis.
- The registry and bus sit behind `ConnectionRegistry` and `GatewayBus` interfaces.
- Stale or missing entries are tolerated: pub/sub is fire-and-forget and sync repairs losses (ADR-005).
- Targeted operations (force disconnect on session revocation or group removal) use the same per-gateway channel.

## Alternatives
- **Per-conversation channels** (gateways subscribe when a local member connects): one publish per message and no per-message registry lookup, better for large groups. Rejected for v1 for subscription-management complexity and authorization on subscribe; revisit if group fan-out becomes the bottleneck.
- **NATS:** lighter and designed for this; adds a component. Candidate if Redis pub/sub is limiting. The interface makes this a contained change.
- **Direct gateway-to-gateway calls:** needs service discovery to pod addresses.
- **Kafka topic per gateway:** high operational cost, lag, wrong tool.

## Consequences
+ Small, understandable, no new infrastructure.
- Redis pub/sub has no persistence or backpressure; Redis Cluster pub/sub behavior needs checking before any clustered deployment (verify in Phase 4A).
- Redis becomes a latency dependency for cross-gateway delivery, though not a correctness dependency.
