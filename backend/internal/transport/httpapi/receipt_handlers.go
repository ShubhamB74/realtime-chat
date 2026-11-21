package httpapi

import (
	"net/http"

	"realtime-chat/backend/internal/auth"
	"realtime-chat/backend/internal/domain"
)

type receiptDTO struct {
	UserID       string `json:"user_id"`
	DeliveredSeq int64  `json:"delivered_seq"`
	ReadSeq      int64  `json:"read_seq"`
}

// markRead moves the caller's read cursor forward (REST twin of the WebSocket read.update, but
// applied immediately rather than coalesced). The cursor never moves backwards and is capped at
// the conversation counter.
func (s *Server) markRead(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	var in struct {
		UpToSeq *int64 `json:"up_to_seq"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if in.UpToSeq == nil {
		s.fail(w, r, domain.Invalid("up_to_seq is required"))
		return
	}
	if err := s.Receipts.MarkRead(r.Context(), id.UserID, r.PathValue("id"), *in.UpToSeq); err != nil {
		s.fail(w, r, err)
		return
	}
	noContent(w)
}

// listReceipts is the on-demand "seen by" view: every member's delivered and read position.
func (s *Server) listReceipts(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	rs, err := s.Receipts.List(r.Context(), id.UserID, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]receiptDTO, 0, len(rs))
	for _, x := range rs {
		out = append(out, receiptDTO{UserID: x.UserID, DeliveredSeq: x.Delivered, ReadSeq: x.Read})
	}
	writeJSON(w, http.StatusOK, map[string]any{"receipts": out})
}
