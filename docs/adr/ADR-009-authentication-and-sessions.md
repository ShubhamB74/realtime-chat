# ADR-009: Authentication tokens and sessions

Status: Proposed (Phase 2)
Date: 2026-11-14

## Context
Phase 2 needs registration, login, short-lived access tokens, refresh-token rotation, session revocation and device registration (docs/LLD.md section 7). Choices below trade simplicity, revocation latency and failure behavior.

## Decision
**Passwords:** Argon2id (`golang.org/x/crypto/argon2`), PHC-format strings, defaults m=64 MiB, t=3, p=2, tunable by env. A semaphore bounds concurrent hashing so a login burst cannot exhaust memory. Stored hashes carry their own parameters, so parameters can be raised and users are re-hashed transparently at next login. Unknown emails still perform a verification against a dummy hash to equalize timing. Passwords: 10 to 128 characters, no composition rules (length matters more).

**Access tokens:** signed JWT, HS256, 15 minutes, claims `sub` (user), `sid` (session family id), `did` (device id), `iss`, `iat`, `exp`. Verification pins the algorithm, issuer and requires `exp`. The secret is at least 32 bytes and comes from the environment.

**Refresh tokens:** 256-bit random opaque strings. Only the SHA-256 digest is stored (a fast hash is appropriate for high-entropy tokens). Each use rotates the token: the old row gets `rotated_at`, a new row is inserted in the same **family**. The family id is the stable session identity. Sliding TTL (30 days) capped by an absolute family lifetime (90 days).

**Reuse detection (strict):** presenting an already-rotated token revokes the entire family, including the newest token. The row is locked (`FOR UPDATE`) so concurrent refreshes with one token yield exactly one success.

**Revocation:** logout, per-session revoke, per-device revoke, and reuse detection all set `revoked_at`. Disabled users are refused at login and refresh.

**Rate limiting:** Redis fixed-window counters (atomic Lua script): register 10/h per IP, login 30/15 min per IP and 10/15 min per email (hashed in the key), refresh 60/min per IP, user lookup 30/min per user, general 300/min per user. **Fails open** if Redis is unavailable (logged).

**Device limit:** at most 20 active devices per user; the user row is locked during login to enforce it.

## Consequences
+ No per-request database hit for access-token verification.
+ Stolen refresh tokens are detected on first replay by either party.
- **Access tokens cannot be revoked before expiry.** After logout, session or device revocation, an access token stays valid up to 15 minutes for REST calls. WebSocket handshakes (Phase 3) will check the session row. Shorten the TTL or add a revocation check if this window is unacceptable.
- **Strict reuse detection can log out a legitimate client** that retries a refresh after losing the response, or two tabs refreshing at once. Clients must serialize refresh and persist the newest token. A short grace window is a possible later change.
- **Fail-open rate limiting** means a Redis outage removes brute-force protection (Argon2 cost still slows attackers). Fail-closed would turn a Redis outage into a login outage, contradicting the principle that Redis is non-authoritative.
- Registration returns 409 for an existing email, which reveals that the address is registered. Avoiding that needs email verification, which is out of scope.
- HS256 uses one shared secret. Rotating it logs everyone out; key IDs and rotation arrive with the multi-service setup (Phase 8) if needed.
- Fixed-window limits allow bursts of up to 2x at window boundaries.

## Alternatives
Opaque access tokens with a session lookup per request (immediate revocation, extra DB or cache hit per request, rejected for now); asymmetric JWT (EdDSA/RS256: useful once another service must verify tokens without being able to mint them, a Phase 7/8 consideration); bcrypt/scrypt (Argon2id preferred); refresh-token reuse grace window (rejected for strictness, revisit with real client behavior).
