# Capacity Estimation (Hypothetical)

Every figure is derived from the stated assumptions. **These are planning estimates, not benchmark results.**

## Assumptions
| Item | Value |
|---|---|
| Registered users | 100,000 |
| DAU | 10,000 |
| Messages per active user per day | 50 |
| Initial concurrent connections | 2,000 |
| Peak-to-average factor | 10x (assumed) |
| Avg stored message size incl. row overhead and indexes | ~1 KB (assumed; text ~200 B) |
| Messages with attachments | 2% at avg 500 KB (assumed) |
| Group traffic | 30% of messages, avg 8 recipients (assumed) |
| Kafka events per message | ~2 (created, plus filtered notification/AI/coalesced receipt events; assumed) |
| Kafka event size | ~1 KB, replication factor 3, retention 7 days |

## Throughput
- Messages/day: 10,000 x 50 = **500,000**
- Average rate: 500,000 / 86,400 = **~5.8 msg/s**
- Peak rate: ~5.8 x 10 = **~58 msg/s** (round to 60)
- Deliveries/s at peak: 0.7 x 60 x 1 + 0.3 x 60 x 8 = 42 + 144 = **~186 recipient deliveries/s**

## Storage
- Messages: 500 MB/day, ~15 GB/month, **~180 GB/year** (before replicas and backups)
- Media: 500,000 x 2% = 10,000 files/day x 0.5 MB = **~5 GB/day, ~1.8 TB/year**
- Outbox: transient; pruned after publish (size depends on retention policy)
- Embeddings (Phase 7, if all content embedded): 1,536-dim float32 is ~6 KB/vector; at one vector per message-group, depends on chunking. Estimate in Phase 7.

## Kafka (derived pipelines only; not in the delivery path)
- Events: 500,000 x ~2 = ~1M/day, avg ~12 events/s, peak ~120 events/s (receipts are coalesced; AI events are filtered by consent)
- Peak ingress: ~120 KB/s (trivial)
- Disk: ~1 GB/day x 7 days x RF 3 = **~21 GB**
- Partitions: chosen for consumer parallelism, not throughput (start ~12; revisit in an ADR). Changing partition count later alters key-to-partition mapping.

## Redis
- Pub/sub fast path: ~60 msg/s peak, a few hundred small messages/s including receipts and typing; far below Redis limits (to verify)
- Connection metadata: 2,000 x ~1 KB = ~2 MB
- Presence/last seen: 10,000 x ~200 B = ~2 MB
- Rate-limit and typing keys: small
- **Budget: well under 256 MB**, memory is not the constraint; Redis is a non-authoritative index.

## WebSocket gateway
- Per connection: goroutines, buffers, bounded outbound queue. Planning assumption ~50-100 KB per connection, so ~100-200 MB for 2,000 (to be measured).

## Database growth and load
- Writes: ~60 inserts/s peak plus outbox and receipts (a few hundred row writes/s at peak), well within a single PostgreSQL primary on modest hardware.
- Receipts are cursors (ADR-007), not rows. Cursor flushes are coalesced in the gateway. Worst case if every connected device flushes every ~5 s: ~2,000 / 5 = ~400 cursor updates/s. Typical far lower. Validate in Phase 4A.
- Per-conversation counter lock: writers to one conversation serialize on commit latency (assume low single-digit ms, so hundreds of msgs/s per hot conversation). Validate in Phase 3.

## Conclusion
At this scale a single PostgreSQL primary, small Kafka cluster, and a few gateway replicas are sufficient. The architecture is deliberately built for horizontal scale to demonstrate the patterns, but sharding is **not justified** by these numbers.
