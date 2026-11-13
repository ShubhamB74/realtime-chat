# Event Catalog

Status: Not started. First populated in Phase 4B.

Topics (prefix `chat.`, versioned): `chat.message.created.v1`, `chat.message.updated.v1`, `chat.receipt.updated.v1` (coalesced), `chat.presence.changed.v1` (optional, sampled), `chat.notification.requested.v1`, `chat.media.uploaded.v1`, `chat.ai.message.received.v1`, `chat.membership.changed.v1`. Key: `conversation_id` for locality; ordering authority is `seq` (ADR-003). Events carry IDs, not bodies, where consumers can fetch authorized content. Online delivery does not use Kafka (ADR-005).
