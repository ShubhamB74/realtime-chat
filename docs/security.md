# Security

Status: Phase 2, 3 and 4A threat model and controls. Controls are implemented in code and tests but **not yet executed** (see docs/phase-2-acceptance-report.md). No external security review has taken place.

## Principles
Least privilege; server-side authorization on every resource; no hardcoded credentials; no message content, secrets or personal data in logs; AI output is untrusted; fail safely.

## Assets
Accounts and credentials; message content (Phase 3+); session tokens; conversation membership (it defines who may read what); uploaded files (Phase 5); AI-derived data (Phase 7).

## Threats and controls (Phase 2)
| Threat | Control | Residual risk |
|---|---|---|
| Credential stuffing, brute force | Argon2id (64 MiB, t=3); per-IP and per-email login limits; generic failure message; timing equalization for unknown emails | Fixed-window limits allow 2x bursts; limiter fails open if Redis is down; no account lockout or breach-password check |
| Password database theft | Argon2id with per-hash salt; parameters stored with each hash and upgraded on login | Parameters need tuning on production hardware |
| Stolen refresh token | 256-bit random tokens stored only as SHA-256; rotation on every use; reuse revokes the whole session | Attacker who refreshes first and is never replayed is not detected |
| Stolen access token | 15-minute lifetime; HS256 with pinned algorithm, issuer and required expiry | **Cannot be revoked before expiry** (ADR-009) |
| JWT forgery (alg=none, wrong key, wrong issuer) | Verification pins HS256, issuer and `exp`; tested | Single shared secret; rotation logs everyone out |
| Accessing others' conversations (IDOR) | Every conversation operation resolves the caller's active membership first; strangers and removed members get the same 404 as a missing id; ids are validated | None known; covered by unit and end-to-end tests |
| Privilege escalation inside a group | Admin-only operations checked server-side; members get 403; role changes and removals checked against the last-admin rule inside a locked transaction | None known |
| Race conditions on membership | Conversation row locked (`FOR UPDATE`) for add/remove/role changes; direct conversation uniqueness enforced by a unique key | None known |
| User enumeration | Login responses and timing are uniform; lookup requires authentication and is rate limited and exact-match only | Registration returns 409 for existing emails (accepted; ADR-009) |
| Abuse of unbounded resources | Body size limit; per-user and per-IP rate limits; group size cap (256); device cap (20); hashing concurrency bound; HTTP server timeouts | Limits are estimates, not yet load-tested |
| Spoofed client IP for rate limits | `X-Forwarded-For` ignored unless `TRUST_PROXY_HEADERS=true`; then the right-most entry is used | Wrong setting behind a different proxy topology weakens limits |
| Secrets in logs or errors | Config logging redacts credentials; JWT secret never logged; error responses never include internals; access log omits query strings and headers; panics log only the type | Needs a log audit in Phase 9 |
| Unauthenticated or hijacked WebSocket | Token required at handshake (header or subprotocol, never the query string); session checked at handshake and every 60 s; origin allow-list for browsers | An open socket outlives access-token expiry until the session is revoked or the next re-check |
| Cross-site WebSocket hijacking | `Origin` must match `WS_ALLOWED_ORIGINS` (or the same host); tokens are not cookies | Wrong allow-list entries weaken this |
| Reading others' messages (IDOR) | Every history, sync, edit, delete and send resolves active membership first and is race-free against removal (conversation row lock); strangers get 404; new members see only post-join messages | None known; covered by unit, store and end-to-end tests |
| Slow-consumer and flooding denial of service | Bounded queue (256 frames, 4 MiB) then disconnect; token bucket per connection plus 1/s sync limit; per-user and global connection caps; read limit 32 KiB; 4000-character messages | Limits are unmeasured; a distributed flood from many accounts is not addressed here |
| Message duplication or loss on retry | Client idempotency keys enforced by a unique index inside the conversation lock | Keys are per device |
| Panics taking the gateway down | Recovered per connection goroutine; only the panic type is logged | |
| Message content in logs | Content is never logged: access log omits bodies and queries; gateway logs ids and counters only | Needs the Phase 9 log audit |
| Frame injection through the gateway bus | The bus is trusted: whoever can PUBLISH to the Redis channels can push arbitrary frames to users' sockets. **Redis must require authentication (`requirepass`/ACLs), TLS where it crosses a network, and be reachable only from the gateways and the API.** Malformed bus payloads are ignored | A compromised or exposed Redis defeats message integrity for live pushes (not storage). Per-message signing is not implemented |
| Forged delivery or read cursors | Validated in SQL: active membership, the device must belong to the caller and not be revoked, values only move forward and are capped at the conversation counter; identity comes from the verified token, never from the payload | None known |
| Presence as a tracking channel | Subscriptions only for users you share an active conversation of at most 50 members with; others silently omitted (no existence leak); 50 subscriptions per connection; no presence in large groups | Anyone you share a small conversation with can see when you are online; there is no "appear offline" setting yet |
| Read receipts reveal behavior | Read position is shared with the other participant in direct conversations, and via the on-demand seen-by view in groups | Always on; no per-user opt-out yet |
| Cursor and receipt flooding | Per-connection event rate limit, coalescing buffer capped at 10,000 entries (excess refused with a retryable error), one transaction per flush | Unmeasured |
| Misconfiguration | Startup validation: secret length, TTL ordering, `sslmode=disable` rejected in production | |
| SQL injection | All queries are parameterized | |

## Not covered yet
Email verification and password reset; MFA; account deletion and data export; audit events (Phase 5); CSRF (not applicable: bearer tokens, no cookies); TLS termination, HSTS and `wss://` (deployment); dependency and container vulnerability scanning (Phase 8); content scanning and moderation (Phase 7); message edit history and retention policy (Phase 6); end-to-end encryption (out of scope, PRD).

## Operational notes
- `AUTH_JWT_SECRET`: at least 32 characters, from a secret manager in production, never committed. Rotating it invalidates all access tokens (refresh tokens still work).
- Tune `ARGON2_*` so one hash takes roughly 100 to 500 ms on production hardware, and keep `ARGON2_MAX_CONCURRENT` times memory within the container's memory limit.
- Expired and revoked `user_sessions` rows are not purged yet (cleanup job planned for Phase 8).
