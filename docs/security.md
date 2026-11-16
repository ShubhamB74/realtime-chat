# Security

Status: Phase 2 threat model and controls. Controls are implemented in code and tests but **not yet executed** (see docs/phase-2-acceptance-report.md). No external security review has taken place.

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
| Misconfiguration | Startup validation: secret length, TTL ordering, `sslmode=disable` rejected in production | |
| SQL injection | All queries are parameterized | |

## Not covered yet
Email verification and password reset; MFA; account deletion and data export; audit events (Phase 5); CSRF (not applicable: bearer tokens, no cookies); TLS termination and HSTS (deployment); dependency and container vulnerability scanning (Phase 8); WebSocket authentication (Phase 3).

## Operational notes
- `AUTH_JWT_SECRET`: at least 32 characters, from a secret manager in production, never committed. Rotating it invalidates all access tokens (refresh tokens still work).
- Tune `ARGON2_*` so one hash takes roughly 100 to 500 ms on production hardware, and keep `ARGON2_MAX_CONCURRENT` times memory within the container's memory limit.
- Expired and revoked `user_sessions` rows are not purged yet (cleanup job planned for Phase 8).
