# Security

Status: Not started. First populated in Phase 2.

Principles: least privilege, server-side authorization, no hardcoded credentials, no content or secrets in logs, AI output untrusted. Media and link previews: fetched only by the media worker with SSRF protections (block private/link-local ranges, resolve-and-pin, size/time limits). AI isolation per ADR-004. Threat model to be written in Phase 2.
