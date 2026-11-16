# Phase 2 Acceptance Report

Date: 2026-11-14. Status: **code written, NOT yet accepted.** It builds on Phase 1, which itself has not been verified on a real machine (no results were reported back). Please complete the Phase 1 verification first, then run the Phase 2 steps below.

## Authoring environment limits
Same as Phase 1: Python and Node only. **No Go toolchain, PostgreSQL, Redis, Docker or network access.** No Go code has been compiled, formatted, linted or run.

## What was actually executed
| Check | Result |
|---|---|
| OpenAPI file parses; all 22 Go routes appear in it with the right method; no extra operations; every `$ref` resolves (script comparing `server.go` to the spec) | Passed |
| Static scan of all 40 Go files: brackets balanced, every import used, no unimported stdlib package referenced (heuristic script, **not** a compiler; it cannot detect type errors) | Passed |
| New migrations (00004 to 00006) have one Up and one Down each and balanced statement markers | Passed |
| `scripts/init-env.sh` now generates a 96-character `AUTH_JWT_SECRET` | Passed |
| Compose file and CI workflow still parse as YAML | Passed |

## Written but NOT executed
Go build and `gofmt`; all unit tests (auth, accounts, chats, httpapi, config, ratelimit); all integration tests (store, Redis limiter, end-to-end API); migrations 00004 to 00006 and every SQL statement in the store layer (SQL has never touched a real PostgreSQL); the Argon2 parameter cost; CI.

Most likely first-run problems: compile errors, `gofmt` whitespace (run `make fmt`), and SQL or parameter-typing details in `internal/store/postgres` (for example UUID parameter casts and `make_interval` arguments).

## Verification procedure
```
git pull / extract the zip over the repo, then:
make deps               # adds golang.org/x/crypto and golang-jwt/jwt/v5; commit go.mod and go.sum
make env                # only if .env predates Phase 2: delete .env and re-run to get AUTH_JWT_SECRET
make fmt && make up
make migrate-up         # expect migrations 4 to 6 applied (6 total)
make migrate-down && make migrate-down && make migrate-down && make migrate-up   # roll back and re-apply
make test               # unit tests
make test-integration   # Postgres + Redis + end-to-end API tests
make lint
make run                # then: make check-health
```
The test most worth watching is `TestUsersCannotAccessConversationsTheyDoNotBelongTo`: it is the acceptance criterion. Send me the output of any failure.

## Requirements status
| Phase 2 requirement | Status |
|---|---|
| Registration, login, Argon2id, short-lived access tokens, refresh rotation, session revocation, device registration, logout, rate limiting | Written |
| User-level authorization, membership, admin permissions, member removal, private conversation access, resource-level checks | Written, covered by unit tests and an end-to-end acceptance test |
| Direct and group conversations, group updates and membership, listing, metadata | Written |
| Last-message preview | **Not implemented**: no messages table exists until Phase 3. The API returns reserved `last_message: null` and `unread_count: 0` |
| Tables: conversations, conversation_members, conversation_settings (with indexes) | Written; indexes are anticipated from query patterns, not measured |
| REST API and OpenAPI documentation | Written (hand-maintained, route coverage tested) |
| "Users must not be able to access resources they are not authorized for" | **Unverified until you run the integration tests** |

## Decisions and deviations to confirm
1. **Added endpoints beyond the brief:** `GET /v1/users/lookup?email=` (needed to start a direct conversation; exact match, rate limited, id and name only) and member role changes (needed for "group administrator permissions").
2. **ADR-009** records the token and session design and its trade-offs. The two to read carefully: access tokens cannot be revoked before they expire (15 min), and refresh-token reuse detection is strict (a client that retries a lost refresh response will be logged out).
3. **Rate limiter fails open** when Redis is down.
4. **`conversation_settings`** holds a conversation-level `ai_enabled` flag (default off) for ADR-004; AI cannot be enabled for direct conversations until both-party consent exists.
5. **Registration reveals whether an email exists** (409). Fixing that needs email verification, which is out of scope.

## Known limitations
- No email verification, password reset, MFA, account deletion or audit log.
- Expired and revoked session rows are never purged.
- Empty groups (everyone left) remain in the database.
- Rate limits are fixed-window and unmeasured; Argon2 parameters are untuned.
- Removed members' access to history is undefined until messages exist (Phase 3 must decide).
- No frontend for any of this yet.
- `last_seq`, `read_seq` and `unread_count` are reserved and unused.
