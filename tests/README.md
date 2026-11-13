# tests/
Cross-service tests only. Unit tests live beside the code they test.
- `integration/` multi-service flows against real Postgres/Redis/Kafka
- `e2e/` browser/client-level scenarios
- `load/` k6 scripts and recorded results (Phase 8)
- `chaos/` failure-injection scenarios (Phase 8)
