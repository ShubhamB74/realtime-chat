# High-Level Design (Revision 2)

Status: Draft for Phase 0 review. Diagrams describe the *intended* design, not an implementation.

Key changes from revision 1: Kafka removed from the online delivery path (ADR-005); sync is the correctness mechanism; receipts are cursors (ADR-007); concrete gateway routing (ADR-006); AI has its own data store (ADR-004); media processing is asynchronous.

## 1. System context
```mermaid
flowchart LR
  U["End User - web browser"] -->|HTTPS + WSS| N["Real-Time Chat Platform"]
  N -->|"LLM API calls (async, optional)"| L["External LLM provider"]
  N -->|"Push notifications"| P["Push provider (mock first)"]
  A["Operator"] -->|"Dashboards"| N
```

## 2. Containers
```mermaid
flowchart TB
  FE["Frontend - React"] -->|REST| API["Go API"]
  FE -->|WebSocket| GW["Go WebSocket Gateway (N replicas)"]
  API --> PG[("PostgreSQL: messages, cursors, outbox")]
  GW --> PG
  GW <-->|"registry + pub/sub bus"| RD[("Redis")]
  API --> MN[("Object store (S3-compatible)")]
  PG --> OB["Outbox Publisher"]
  OB --> KF{{"Kafka (derived pipelines only)"}}
  KF --> NS["Notification Service"]
  KF --> MW["Media Worker: scan, thumbnails, link previews"]
  KF --> AI["AI Service - FastAPI"]
  AI --> AIDB[("AI store: embeddings, tasks (separate DB and credentials)")]
  AI -->|"internal content + authz API"| API
  AI --> LLM["LLM Provider"]
  MW --> MN
```
Online delivery between gateways uses Redis pub/sub, **not** Kafka.

## 3. Dependency criticality
| Component | Needed to accept a message | Needed for online delivery | Needed for correctness (eventual) |
|---|---|---|---|
| Gateway/API + PostgreSQL | Yes | Yes | Yes |
| Redis | No | Cross-gateway only (degrades to sync) | No |
| Outbox + Kafka | No | No | Only for derived features |
| Notification, Media, AI | No | No | No |

Initial deployables: `api`, `gateway`, `worker` roles from one Go codebase, plus `ai-service`. No further services until justified.

## 4. Message lifecycle: fast path and reliable path
```mermaid
sequenceDiagram
  participant C as Client A
  participant G1 as Gateway 1
  participant DB as PostgreSQL
  participant R as Redis
  participant G2 as Gateway 2
  participant B as Client B
  participant O as Outbox Publisher
  participant K as Kafka
  C->>G1: message.send (idempotency key)
  G1->>DB: TX: bump conversation counter, insert message (seq, mod_seq), insert outbox row
  DB-->>G1: commit
  G1-->>C: message.accepted (id, seq)
  par Fast path (best effort)
    G1->>R: lookup member connections
    R-->>G1: Gateway 2 holds Client B
    G1->>R: publish to channel for Gateway 2
    R->>G2: message envelope
    G2->>B: message.received
    B-->>G2: message.ack (up to seq)
    G2->>DB: flush device cursor (coalesced)
  and Reliable path (derived pipelines)
    O->>DB: poll unpublished outbox
    O->>K: publish chat.message.created.v1
    K-->>O: broker ack
    O->>DB: mark published
  end
```
If the fast path fails at any step, the recipient converges through sync (section 5). Duplicates are removed client-side by `(conversation_id, seq)`.

## 5. Reconnect, offline delivery and sync
```mermaid
sequenceDiagram
  participant B as Client B device
  participant G as Gateway
  participant DB as PostgreSQL
  participant K as Kafka
  participant N as Notification Service
  participant R as Redis
  K->>N: chat.message.created.v1
  N->>R: any live connection for recipient?
  R-->>N: none
  N->>B: push (deduplicated, no content in logs)
  B->>G: connect + sync.request (device id)
  G->>DB: conversations where mod_seq > device cursor
  DB-->>G: changed messages
  G-->>B: sync.batch (paged)
  B-->>G: message.ack (up to seq)
  G->>DB: advance device cursor
```
Safety nets: clients detect sequence gaps and request gap-fill; clients run a periodic reconcile sync (assumed every 60 s while connected) to catch trailing lost pushes.

## 6. AI processing
```mermaid
sequenceDiagram
  participant K as Kafka
  participant AI as AI Service
  participant API as Backend internal API
  participant S as AI store
  participant L as LLM Provider
  participant U as User
  K->>AI: chat.ai.message.received.v1 (IDs only, opted-in conversations)
  AI->>API: fetch content (service principal, scoped to AI-enabled conversations)
  AI->>S: create task, store embeddings/results
  AI->>L: request (timeout, retry, circuit breaker)
  L-->>AI: untrusted response, validate structure
  AI->>S: persist result
  U->>AI: assistant question
  AI->>API: which conversations may this user query? (no caching)
  API-->>AI: authorized conversation IDs
  AI->>S: retrieve, then post-filter by authorized IDs
  AI->>L: grounded prompt
  AI-->>U: answer + source message references
```

## 7. Deployment architecture (target, Phase 8)
```mermaid
flowchart TB
  I["Ingress / Load Balancer"] --> GWp["Gateway pods (HPA on connection count)"]
  I --> APIp["API pods (HPA)"]
  GWp & APIp --> PGs[("PostgreSQL primary + replica")]
  GWp & APIp --> RDs[("Redis")]
  WKp["Worker pods"] --> KFs{{"Kafka x3 (KRaft)"}}
  AIp["AI pods"] --> KFs
  AIp --> AIDBs[("AI database")]
  APIp --> MNs[("Object store (S3-compatible)")]
  OTel["OTel Collector"] --> PR["Prometheus"] --> GF["Grafana"]
```
Deploy behavior: gateways drain connections gracefully (long termination grace period), clients reconnect with exponential backoff and jitter to avoid reconnect storms.

## 8. Ordering and delivery guarantees (see ADR-003, ADR-005)
- **Accepted:** committed in PostgreSQL. Not lost on single-node failure (for the tested durability configuration).
- **Display order:** `(conversation_id, seq)`, assigned inside the commit transaction, gapless, commit-ordered.
- **Change order for sync:** `mod_seq` from the same per-conversation counter (edits and deletes get a new `mod_seq`).
- **Kafka partitioning** by `conversation_id` gives consumers locality and per-conversation publish order, but is **not** the ordering authority.
- **Delivery to devices:** at-least-once, effectively-once after client dedupe.
- **Not guaranteed:** ordering across conversations; presence and typing are best-effort and ephemeral.

## 9. Presence and typing
Presence lives in Redis with TTL refreshed by heartbeats, plus a short grace period before declaring offline to avoid flapping. Clients subscribe to presence only for what is visible (open conversation, visible list items, capped), and groups above a size threshold show no presence. Typing indicators are ephemeral pub/sub only, rate-limited, never persisted.

## 10. Key risks
1. **Hot conversations:** the counter row lock serializes writers per conversation. Throughput per conversation is bounded by commit latency. Measure in Phase 3.
2. **Cursor write volume:** coalesce cursor flushes in the gateway; validate in Phase 4A.
3. **Redis pub/sub is fire-and-forget:** acceptable only because sync repairs losses; the reconcile interval bounds worst-case staleness.
4. **Outbox lag** affects derived features only; monitor it.
5. **AI authorization drift:** mitigated by no-cache query-time authorization plus post-filtering.
6. **Link previews and media fetches:** SSRF risk, handled in the media worker (Phase 5).
