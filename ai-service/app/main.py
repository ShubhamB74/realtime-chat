"""AI service entry point. Phase 1: health endpoints only.

This service is asynchronous and must never be on the message delivery path (ADR-001, ADR-004).
"""

from fastapi import FastAPI


def create_app() -> FastAPI:
    app = FastAPI(title="AI Service", version="0.1.0")

    @app.get("/healthz")
    def healthz() -> dict[str, str]:
        """Liveness: the process is up. Does not touch dependencies."""
        return {"status": "ok"}

    @app.get("/readyz")
    def readyz() -> dict[str, str]:
        """Readiness: no dependencies yet (own store and Kafka arrive in Phase 7)."""
        return {"status": "ready"}

    return app


app = create_app()
