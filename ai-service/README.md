# ai-service/
Python + FastAPI. Asynchronous only. It MUST NOT be on the message delivery path (ADR-001, ADR-004).

**Belongs here:** LLM provider interface and adapters, prompt management, Kafka consumers for AI events, summarization, embeddings, RAG, smart replies, moderation, token and cost tracking, its own store (separate database/credentials).
**Does not belong here:** message acceptance or delivery, or direct access to the messaging schema. Content and authorization come from the backend internal API.

**Status:** Not started (skeleton in Phase 1, features in Phase 7).
