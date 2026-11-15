# ADR-008: Object storage (replacing MinIO)

Status: Proposed (new, needs your confirmation)
Date: 2026-11-13

## Context
The project instructions specify MinIO. During Phase 1 version checks, multiple sources reported that the MinIO community edition is no longer maintained: community Docker images and binaries stopped in late 2025, the project went into maintenance mode on 2025-12-03, and the repository was archived in 2026 (reports give 2026-04-25; some say it was first archived in February). MinIO's remaining free offering is under a proprietary license that restricts redistribution. These are secondary sources; confirm on the MinIO repository before relying on the details. A new project should not start on an unmaintained storage server, especially one that will handle uploaded user files (Phase 5).

## Decision
- The application talks to object storage through the **S3 API only**, behind a small interface in the Go code. No vendor-specific features.
- **Development default: SeaweedFS** (Apache 2.0, single `server -s3` command, simple in Compose). Dev mode runs without S3 credentials and is bound to localhost only.
- **Candidates for staging/production** (decided in Phase 8): Garage, SeaweedFS, Ceph RGW, or a managed S3-compatible service. Garage is a strong alternative (single binary, AGPL-3.0) if dev/prod parity matters more than setup simplicity.
- The compose service is named `objectstore` and config uses `OBJECT_STORE_*`, so the backing product can change without touching application code.

## Consequences
+ No dependency on an unmaintained project; swapping providers is a config change.
- Signed upload/download URLs (Phase 5) need credentials: the dev setup must gain an S3 credential file then.
- The SeaweedFS image tag is not yet pinned (marked TODO in compose). Pin it after verifying the current release.
- Less common in tutorials than MinIO; expect some self-service troubleshooting.

## Alternatives considered
Keep MinIO from source (supply-chain and patching burden, unmaintained); build MinIO from a community fork (unaudited); LocalStack-style S3 mocks (good for tests, not for dev/prod parity); S3Proxy (tests only).
