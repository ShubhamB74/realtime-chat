package httpapi

import (
	"net/http"
	"strings"

	"realtime-chat/backend/internal/app/chats"
	"realtime-chat/backend/internal/auth"
	"realtime-chat/backend/internal/domain"
)

// normalizeUUID validates and lowercases a UUID taken from a path, query or body.
func normalizeUUID(raw string) (string, error) { return chats.NormalizeID(raw) }

func detailDTO(d chats.Detail) conversationDTO {
	return toConversation(d.Conversation, d.MemberCount, d.Role, nil, nil)
}

func (s *Server) openDirect(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	var in struct {
		UserID string `json:"user_id"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	d, created, err := s.Chats.OpenDirect(r.Context(), id.UserID, in.UserID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"conversation": detailDTO(d)})
}

func (s *Server) createGroup(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	var in struct {
		Title     string   `json:"title"`
		MemberIDs []string `json:"member_ids"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	d, err := s.Chats.CreateGroup(r.Context(), id.UserID, in.Title, in.MemberIDs)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"conversation": detailDTO(d)})
}

func (s *Server) listConversations(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	limit, err := parseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	cursor, err := decodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, next, err := s.Chats.List(r.Context(), id.UserID, cursor, limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]conversationDTO, 0, len(items))
	for _, it := range items {
		dto := toConversation(it.Conversation, it.MemberCount, it.Role, it.Peer, it.LastMessage)
		dto.UnreadCount = it.Unread
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": out, "next_cursor": encodeCursor(next)})
}

func (s *Server) getConversation(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	d, err := s.Chats.Get(r.Context(), id.UserID, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversation": detailDTO(d)})
}

func (s *Server) renameConversation(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	var in struct {
		Title *string `json:"title"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if in.Title == nil {
		s.fail(w, r, domain.Invalid("title is required"))
		return
	}
	d, err := s.Chats.Rename(r.Context(), id.UserID, r.PathValue("id"), *in.Title)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversation": detailDTO(d)})
}

func (s *Server) listMembers(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	ms, err := s.Chats.Members(r.Context(), id.UserID, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]memberDTO, 0, len(ms))
	for _, m := range ms {
		out = append(out, toMember(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": out})
}

func (s *Server) addMembers(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	var in struct {
		UserIDs []string `json:"user_ids"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	added, err := s.Chats.AddMembers(r.Context(), id.UserID, r.PathValue("id"), in.UserIDs)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]memberDTO, 0, len(added))
	for _, m := range added {
		out = append(out, toMember(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": out})
}

func (s *Server) removeMember(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	if err := s.Chats.RemoveMember(r.Context(), id.UserID, r.PathValue("id"), r.PathValue("userId")); err != nil {
		s.fail(w, r, err)
		return
	}
	noContent(w)
}

func (s *Server) setRole(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	var in struct {
		Role string `json:"role"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.Chats.SetRole(r.Context(), id.UserID, r.PathValue("id"), r.PathValue("userId"), strings.TrimSpace(in.Role)); err != nil {
		s.fail(w, r, err)
		return
	}
	noContent(w)
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	st, err := s.Chats.Settings(r.Context(), id.UserID, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": map[string]any{"ai_enabled": st.AIEnabled}})
}

func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	var in struct {
		AIEnabled *bool `json:"ai_enabled"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if in.AIEnabled == nil {
		s.fail(w, r, domain.Invalid("ai_enabled is required"))
		return
	}
	st, err := s.Chats.SetAIEnabled(r.Context(), id.UserID, r.PathValue("id"), *in.AIEnabled)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": map[string]any{"ai_enabled": st.AIEnabled}})
}
