# Roadmap (Revised)

Changes from the original instruction file, and why.

## Principles behind the reordering
1. Get a thin send-to-receive slice working early (single gateway, no Kafka) so design decisions are driven by running code.
2. Establish a k6 harness and baseline numbers in Phase 3, not Phase 8. Phase 8 validates; it does not discover.
3. Kafka arrives only where it earns its place: derived pipelines. Delivery correctness never depends on it (ADR-005).
4. Split the oversized original Phase 4.

## Phases
| Phase | Scope | Exit criteria |
|---|---|---|
| 0 | Requirements, HLD, LLD, ADRs, repo | Checklist approved |
| 1 (code written, **awaiting your local verification**) | Docker Compose with profiles (`core`: Postgres, Redis, object store; `streaming`: Kafka + UI), Go/Python/TS skeletons, migrations (users, sessions, devices), CI | Clone-and-run documented and executed |
| 2 (code written, **awaiting your local verification**) | Auth (Argon2id, rotation, revocation), authorization, conversations | Negative authorization tests pass |
| 3 | **Thin slice**: WS gateway (bounded queues, heartbeats), message persistence with `seq`/`mod_seq`, idempotency, history, cursor sync, edit/delete, minimal presence, **k6 harness + baseline** | Messages survive restart; reconnect recovers via sync; baseline recorded |
| 4A | Fast path over Redis pub/sub, connection registry, cross-gateway delivery, device cursors, receipts as cursors, unread counts, presence subscriptions | Two gateways deliver across each other; Redis outage degrades to sync without loss |
| 4B | Transactional outbox, Kafka topics, publisher, idempotent consumers, retries, DLQ | Worker/broker failure loses nothing; duplicates harmless |
| 5 | Notifications (consume Kafka), async media pipeline (scan, thumbnails, link previews with SSRF protection), reliability patterns | Reliability tests pass |
| 6 | PostgreSQL FTS, multi-device completion, session revocation sync, retention and deletion policy | Consistency doc matches behavior |
| 7 | AI service with own store, content API integration, RAG, summaries, smart replies, moderation, evals | Messaging unaffected with all AI providers down |
| 8 | Kubernetes (HPA on connection count, connection draining), observability, full load and chaos, DB scalability, CD | Reports with p50/p95/p99 and recovery results |
| 9 | Final review and portfolio | Evidence-backed claims only |

## Mapping to the original instruction file
| Original | Now |
|---|---|
| Phase 3 (WebSocket + persistence) | Phase 3 (adds sync, `seq`/`mod_seq`, early k6) |
| Phase 4 (Kafka + distributed delivery) | Split: 4A delivery/sync/receipts, 4B outbox/Kafka |
| Phase 5-9 | Unchanged numbering; scopes adjusted as above |

The original instruction file should be updated to match (Phase 3, Phase 4 split, no gRPC, AI own store, Kafka not in the delivery path).
