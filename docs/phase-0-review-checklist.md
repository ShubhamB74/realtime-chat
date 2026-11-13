# Phase 0 Review Checklist (Revision 2)

Implementation (Phase 1) starts only after these are confirmed.

- [ ] PRD scope and out-of-scope list approved (incl. no E2EE)
- [ ] PRD open questions and product-model decisions answered
- [ ] NFR targets and assumptions accepted
- [ ] Capacity assumptions accepted (peak factor, message size, group size)
- [ ] HLD revision 2 reviewed (fast path + sync, Kafka off the delivery path)
- [ ] ADR-001 to ADR-007 accepted or revised
- [ ] Sync model confirmed: messages stored once, `seq`/`mod_seq`, device cursors
- [ ] gRPC removal confirmed
- [ ] Roadmap (4A/4B split, early k6) approved
- [ ] Monorepo structure approved
