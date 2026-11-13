# Product Requirements Document

Status: Draft for Phase 0 review.

## 1. Vision
A reliable real-time chat product where messages are never lost once accepted, and where optional AI features (summaries, semantic search, smart replies, moderation) add value without ever affecting core delivery.

## 2. Users and goals
- **End user:** talk to individuals and groups across multiple devices, with history, search, and notifications.
- **Group admin:** manage membership and group settings.
- **Moderator (later):** review AI-flagged content.

## 3. Scope (see functional-requirements.md for IDs)
Registration/login, 1:1 and group conversations, real-time messaging, edit/delete, delivery and read receipts, presence, typing indicators, multi-device, media attachments, search, notifications, AI assistant, summarization, semantic search, smart replies, AI moderation.

## 4. Out of scope (initially)
Voice/video calls, end-to-end encryption, federation, multi-region active-active, native mobile apps (web first; push is abstracted).

> End-to-end encryption is out of scope because server-side search, AI, and moderation require server access to content. This is a deliberate trade-off recorded in ADR-004; it must be stated clearly to users.

## 5. Success criteria
- Accepted messages survive process and single-node failures (see NFRs).
- Messaging works with every AI provider down.
- Every reliability or performance claim is backed by a recorded test.

## 6. Product-model decisions needed (new)
- Workspaces/organizations: not in v1 (assumed); confirm.
- Retention: configurable per conversation? Indefinite default?
- Deletion: user data deletion and AI artifact purge (ADR-004) must be specified in Phase 6.
- Link previews: in scope? (SSRF-sensitive; Phase 5)

## 7. Open questions for review
1. Is single-region deployment acceptable for the project's lifetime?
2. Message retention: indefinite, or configurable per workspace?
3. Edit/delete semantics: soft delete with tombstones (assumed) vs hard delete?
4. Maximum group size (assumed 256 for capacity estimates).
5. Is a managed LLM provider acceptable, or must a self-hosted option exist from the start?
