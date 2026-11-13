# Functional Requirements

Priority: M = must, S = should, C = could. Phase = planned delivery phase.

| ID | Requirement | Pri | Phase |
|---|---|---|---|
| FR-AUTH-1 | Register with email + password | M | 2 |
| FR-AUTH-2 | Login; short-lived access token, rotating refresh token | M | 2 |
| FR-AUTH-3 | Logout and session revocation per device | M | 2 |
| FR-CONV-1 | Create/get one-to-one conversation (unique per user pair) | M | 2 |
| FR-CONV-2 | Create group; admins add/remove members, update metadata | M | 2 |
| FR-CONV-3 | List conversations with last-message preview and unread count | M | 2/4 |
| FR-MSG-1 | Send message in real time over WebSocket | M | 3 |
| FR-MSG-2 | Message persisted before acceptance ack | M | 3 |
| FR-MSG-3 | Client idempotency key prevents duplicates | M | 3 |
| FR-MSG-4 | Edit own message; delete own message (admins may delete in groups) | M | 3 |
| FR-MSG-5 | Cursor-paginated history | M | 3 |
| FR-MSG-6 | Per-conversation total order via DB-assigned sequence | M | 4 |
| FR-RCPT-1 | Delivered and read receipts per device/user | M | 4 |
| FR-PRES-1 | Online/offline presence, last seen, multi-device aware | S | 3 |
| FR-TYPE-1 | Typing indicators (ephemeral, not persisted) | S | 3 |
| FR-DEV-1 | Multiple devices per user with independent delivery state | M | 4/6 |
| FR-MEDIA-1 | Upload/download images and files via signed URLs, access-controlled | M | 5 |
| FR-SRCH-1 | Keyword search with conversation, date, sender filters | M | 6 |
| FR-NOTIF-1 | Notify offline users; preferences; dedup | S | 5 |
| FR-AI-1 | Conversation and unread summaries, action items, decisions | S | 7 |
| FR-AI-2 | Semantic search, authorization-filtered | S | 7 |
| FR-AI-3 | RAG assistant answering with source message references | S | 7 |
| FR-AI-4 | Smart reply suggestions; never auto-sent | S | 7 |
| FR-AI-5 | Moderation: classify and flag, then policy enforcement and human review | C | 7 |
| FR-AI-6 | User-level and conversation-level AI opt-in/opt-out | M | 7 |
