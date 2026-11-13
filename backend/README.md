# backend/
Go core: HTTP API, WebSocket gateway, delivery workers, outbox publisher, notification and media services.

Planned layout (to be confirmed in Phase 1):
```
cmd/            # entrypoints: api, gateway, worker (one binary per deployable role)
internal/
  domain/       # business logic, no infrastructure imports
  app/          # use cases / services
  transport/    # http, ws, grpc handlers
  store/        # postgres, redis, kafka, minio adapters
  platform/     # config, logging, telemetry, shutdown
migrations/     # SQL migrations (forward + rollback)
```
Rule: domain depends on nothing; adapters depend on domain.

**Status:** Not started (Phase 1).
