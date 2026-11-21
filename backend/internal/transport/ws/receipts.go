package ws

import (
	"context"
	"sync"
	"time"

	"realtime-chat/backend/internal/domain"
)

// Delivery acknowledgements and read marks are coalesced here and written in batches
// (ADR-007). Many acks for the same device and conversation collapse to the highest value, so
// the database sees at most one write per active (device, conversation) per flush interval.
// Buffered cursors are lost if the process crashes; that only causes clients to re-acknowledge
// and re-receive some messages, which they deduplicate.

const maxBufferedCursors = 10000

type ackKey struct{ user, device, conv string }
type readKey struct{ user, conv string }

type cursorBuffer struct {
	mu    sync.Mutex
	acks  map[ackKey]int64
	reads map[readKey]int64
}

// ack records the highest acknowledged cursor; it reports false if the buffer is full.
func (b *cursorBuffer) ack(k ackKey, v int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.acks == nil {
		b.acks = map[ackKey]int64{}
	}
	if _, exists := b.acks[k]; !exists && len(b.acks)+len(b.reads) >= maxBufferedCursors {
		return false
	}
	if v > b.acks[k] || !contains(b.acks, k) {
		b.acks[k] = v
	}
	return true
}

func contains[K comparable](m map[K]int64, k K) bool { _, ok := m[k]; return ok }

// read records the highest read mark; it reports false if the buffer is full.
func (b *cursorBuffer) read(k readKey, v int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.reads == nil {
		b.reads = map[readKey]int64{}
	}
	if _, exists := b.reads[k]; !exists && len(b.acks)+len(b.reads) >= maxBufferedCursors {
		return false
	}
	if v > b.reads[k] || !contains(b.reads, k) {
		b.reads[k] = v
	}
	return true
}

func (b *cursorBuffer) drain() ([]domain.DeviceAck, []domain.ReadMark) {
	b.mu.Lock()
	acks, reads := b.acks, b.reads
	b.acks, b.reads = nil, nil
	b.mu.Unlock()
	outA := make([]domain.DeviceAck, 0, len(acks))
	for k, v := range acks {
		outA = append(outA, domain.DeviceAck{UserID: k.user, DeviceID: k.device, ConversationID: k.conv, UpToModSeq: v})
	}
	outR := make([]domain.ReadMark, 0, len(reads))
	for k, v := range reads {
		outR = append(outR, domain.ReadMark{UserID: k.user, ConversationID: k.conv, UpToSeq: v})
	}
	return outA, outR
}

func (h *Hub) flushLoop() {
	defer h.bgWG.Done()
	t := time.NewTicker(h.cfg.FlushInterval)
	defer t.Stop()
	for {
		select {
		case <-h.bgCtx.Done():
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(h.bgCtx, 5*time.Second)
			h.flushCursors(ctx)
			cancel()
		}
	}
}

// flushCursors writes everything buffered. A failed batch is dropped (not retried): clients
// acknowledge again after their next batch, and cursors only move forward.
func (h *Hub) flushCursors(ctx context.Context) {
	if h.receipts == nil {
		return
	}
	acks, reads := h.cursors.drain()
	if len(acks) == 0 && len(reads) == 0 {
		return
	}
	if err := h.receipts.Apply(ctx, acks, reads); err != nil {
		h.warnThrottled("could not store delivery/read cursors; clients will re-acknowledge", err)
	}
}

// PublishReceipt implements receipts.Publisher. Direct conversations push receipt changes to
// both participants; in groups only the member's own devices are told about read progress, and
// "seen by" is available on demand over REST (ADR-007). Unchanged states are not re-pushed.
func (h *Hub) PublishReceipt(st domain.ReceiptState) {
	key := st.ConversationID + "/" + st.UserID
	cur := [2]int64{st.Delivered, st.Read}

	h.receiptMu.Lock()
	last, seen := h.lastReceipt[key]
	if seen && last == cur {
		h.receiptMu.Unlock()
		return
	}
	if len(h.lastReceipt) >= 20000 {
		h.lastReceipt = map[string][2]int64{}
	}
	h.lastReceipt[key] = cur
	readChanged := !seen || last[1] != cur[1]
	h.receiptMu.Unlock()

	var recipients []string
	if st.Direct {
		recipients = append(recipients, st.Others...)
	}
	if readChanged {
		recipients = append(recipients, st.UserID) // the member's other devices sync their unread state
	}
	if len(recipients) == 0 {
		return
	}
	frame, err := newEvent(EvReceipt, "", receiptUpdate{
		ConversationID: st.ConversationID, UserID: st.UserID, DeliveredSeq: st.Delivered, ReadSeq: st.Read,
	})
	if err != nil {
		return
	}
	h.fanout(recipients, frame, "")
}
