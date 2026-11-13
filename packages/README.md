# packages/
Shared contracts, the only code shared across languages.
- `event-schemas/` versioned Kafka event schemas (additive within a version; see docs/event-catalog.md)
- `openapi/` REST specification (source for generated clients)

gRPC/proto definitions are not part of the initial scope (ADR-002). Schema changes need an ADR or catalog entry. No silent changes.
