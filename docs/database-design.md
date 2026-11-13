# Database Design

Status: Not started. First populated in Phase 1.

Planned core: users, user_sessions, devices (Phase 1); conversations (with `last_seq`), conversation_members (with `read_seq`), messages (`seq`, `mod_seq`), device_cursors, outbox_events (see LLD sections 2-3). The AI store is a separate database (ADR-004).
