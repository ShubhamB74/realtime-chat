# Failure Scenarios

Status: Not started. First populated in Phase 4A.

Expected behaviors to verify: Redis down (cross-gateway fast path lost, sync repairs, bounded by reconcile interval); gateway crash (clients reconnect with jittered backoff, sync); Kafka down (derived features delayed, delivery unaffected); PostgreSQL down (acceptance fails, no false acks); AI down (messaging unaffected). Results will be recorded from actual tests only.
