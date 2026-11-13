# ADR-004: AI privacy and data access architecture

Status: Proposed (revision 2)
Date: 2026-11-13

## Decision
AI is opt-in, asynchronous, access-controlled, and never on the delivery path.

- Consent: no message reaches an LLM without user-level opt-in and conversation-level enablement. Feature-specific triggers only.
- **Own data store.** The AI service uses a separate database (or at minimum a separate schema with its own credentials) for embeddings, tasks, and results. It has no credentials for the messaging schema and does not read it directly.
- **Content access** goes through a backend internal API, using a service principal scoped to AI-enabled conversations. Kafka events carry IDs, not message bodies.
- **Query-time authorization:** for every user-initiated retrieval, the AI service asks the backend which conversations that user may query, with **no caching**, then retrieves and **post-filters** results against that list. Removing someone from a group therefore revokes their AI access immediately.
- Deletion: message deletes/edits trigger purge or re-embed; consent withdrawal triggers a purge workflow covering embeddings, summaries, and task results.
- LLM output is untrusted: structured validation, no auto-sending, sanitized rendering.
- Prompt-injection defenses: separate instructions from retrieved content, treat messages as data, constrain actions, filter outputs.
- Logs exclude raw content and secrets; token and cost metrics only.
- Provider abstraction allows swapping providers, including self-hosted models.

## Trade-offs
- Not end-to-end encrypted; AI, search, and moderation need server access. This must be disclosed to users.
- An external provider sees opted-in content. Mitigated by opt-in, minimization, and provider choice, not eliminated.
- Query-time authorization adds one internal call per AI query; acceptable.

## Acceptance
All messaging works with every AI provider unavailable (chaos test, Phase 8).
