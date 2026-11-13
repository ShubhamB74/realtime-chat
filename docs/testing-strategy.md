# Testing Strategy

Status: Not started. First populated in Phase 1.

Unit tests beside code; integration tests against real dependencies; contract tests for events and APIs; **k6 harness from Phase 3 with a recorded baseline**; chaos tests. Specific tests: client reducer dedupe/gap/reconcile, concurrent sends to one conversation (gapless seq), idempotent retries, sync after Redis outage. Nothing is reported as passing unless executed.
