# Phase 0: documentation targets only. Build/test/lint targets arrive in Phase 1.
.PHONY: help docs-check
help:
	@echo "Targets:"
	@echo "  docs-check  Verify all required documentation files exist"

REQUIRED_DOCS = README.md docs/PRD.md docs/functional-requirements.md \
 docs/non-functional-requirements.md docs/HLD.md docs/LLD.md docs/database-design.md \
 docs/api-specification.md docs/event-catalog.md docs/security.md docs/scalability.md \
 docs/failure-scenarios.md docs/testing-strategy.md docs/deployment.md docs/observability.md

docs-check:
	@missing=0; for f in $(REQUIRED_DOCS); do [ -f $$f ] || { echo "MISSING: $$f"; missing=1; }; done; \
	[ -d docs/adr ] || { echo "MISSING: docs/adr/"; missing=1; }; \
	[ $$missing -eq 0 ] && echo "All required docs present."
