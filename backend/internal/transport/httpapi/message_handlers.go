package httpapi

import (
	"log/slog"
	"net/http"
	"strconv"

	"realtime-chat/backend/internal/app/messages"
	"realtime-chat/backend/internal/auth"
	"realtime-chat/backend/internal/domain"
	"realtime-chat/backend/internal/transport/wire"
)

// presenceMaxMembers: above this size presence is not exposed (docs/adr/ADR-007).
const presenceMaxMembers = 50

func parseNonNegInt64(raw, name string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, domain.Invalid(name + " must be a non-negative integer")
	}
	return n, nil
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	var in struct {
		IdempotencyKey string `json:"idempotency_key"`
		Body           string `json:"body"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	out, err := s.Messages.Send(r.Context(), messages.SendInput{
		UserID: id.UserID, DeviceID: id.DeviceID, ConversationID: r.PathValue("id"),
		IdempotencyKey: in.IdempotencyKey, Body: in.Body,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusCreated
	if out.Duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"message": wire.FromMessage(out.Message), "duplicate": out.Duplicate})
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	before, err := parseNonNegInt64(r.URL.Query().Get("before_seq"), "before_seq")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	limit, err := parseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	msgs, more, err := s.Messages.History(r.Context(), id.UserID, r.PathValue("id"), before, limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": wire.FromMessages(msgs), "has_more": more})
}

func (s *Server) listChanges(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	after, err := parseNonNegInt64(r.URL.Query().Get("after_mod_seq"), "after_mod_seq")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	limit, err := parseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ch, err := s.Messages.Changes(r.Context(), id.UserID, r.PathValue("id"), after, limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"changes": wire.FromMessages(ch.Messages), "has_more": ch.HasMore, "last_seq": ch.LastSeq})
}

func (s *Server) editMessage(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	var in struct {
		Body string `json:"body"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	m, err := s.Messages.Edit(r.Context(), id.UserID, r.PathValue("id"), r.PathValue("messageId"), in.Body)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": wire.FromMessage(m)})
}

func (s *Server) deleteMessage(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	m, err := s.Messages.Delete(r.Context(), id.UserID, r.PathValue("id"), r.PathValue("messageId"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": wire.FromMessage(m)})
}

// conversationPresence lists which members are online. Members only; unavailable for large
// groups and whenever the presence store is down (presence is best effort, never an error).
func (s *Server) conversationPresence(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	members, err := s.Chats.Members(r.Context(), id.UserID, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	unavailable := map[string]any{"available": false, "online": []string{}}
	if s.Presence == nil || len(members) > presenceMaxMembers {
		writeJSON(w, http.StatusOK, unavailable)
		return
	}
	ids := make([]string, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.UserID)
	}
	on, err := s.Presence.Online(r.Context(), ids)
	if err != nil {
		s.Logger.WarnContext(r.Context(), "presence lookup failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusOK, unavailable)
		return
	}
	online := make([]string, 0, len(ids))
	for _, u := range ids {
		if on[u] {
			online = append(online, u)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "online": online})
}
