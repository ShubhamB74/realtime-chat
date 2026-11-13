# Non-Functional Requirements

**All values are hypothetical targets. None have been measured.** Reference assumptions: single region, 3-node Kubernetes cluster of 4 vCPU / 16 GB nodes, managed-equivalent PostgreSQL with one synchronous or fast-commit primary, 3-broker Kafka, workload per [capacity-estimation.md](capacity-estimation.md). These will be revised after Phase 8 benchmarks.

| ID | Area | Target | Measured by |
|---|---|---|---|
| NFR-LAT-1 | Message acceptance latency (client send to `message.accepted`) | p95 < 150 ms, p99 < 300 ms at 60 msg/s | k6 + server histogram |
| NFR-LAT-2 | End-to-end fast-path delivery to an online recipient, same region | p95 < 300 ms, p99 < 800 ms | k6 |
| NFR-LAT-3 | Derived pipelines (notification request, indexing) after commit | p95 < 5 s | Outbox lag metric |
| NFR-SYNC-1 | Reconnect sync: 100 active conversations, 1,000 missed messages | p95 < 2 s | k6 |
| NFR-SYNC-2 | Worst-case staleness after fast-path loss | <= reconcile interval (assumed 60 s) | Chaos tests |
| NFR-DEP-1 | Gateway rolling deploy | No message loss; reconnect storm absorbed via jittered backoff | k6 + deploy test |
| NFR-CAP-1 | Concurrent WebSocket connections | 2,000 initial; design headroom to 20,000 (per assumptions above) | k6 |
| NFR-CAP-2 | Concurrent active users | 2,000 initial | k6 |
| NFR-AVL-1 | API/gateway availability | 99.9% monthly (about 43 min downtime) | SLO dashboards |
| NFR-DUR-1 | Durability of accepted messages | No loss on single process or single node failure (RPO = 0) | Chaos tests |
| NFR-DUR-2 | Regional disaster | RPO <= 5 min via WAL archiving and backups | Restore drills |
| NFR-REC-1 | RTO for single component failure | < 5 min automated | Chaos tests |
| NFR-REC-2 | RTO for database failover | < 15 min | Failover drills |
| NFR-SEC-1 | Password hashing | Argon2id with tuned parameters | Code review/test |
| NFR-SEC-2 | AuthZ | Server-enforced on every resource access; negative tests per endpoint | Integration tests |
| NFR-SEC-3 | Logs | No message content, secrets, or PII | Log audit tests |
| NFR-SCL-1 | Horizontal scaling | Gateways and workers stateless; scale by replicas | Load tests |
| NFR-AI-1 | AI isolation | Messaging fully functional with all AI providers down | Chaos test |
| NFR-AI-2 | AI latency (summary, async) | Informational: p95 < 30 s, not a delivery SLO | AI metrics |
