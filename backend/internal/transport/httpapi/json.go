package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"time"

	"realtime-chat/backend/internal/domain"
	"realtime-chat/backend/internal/platform/httpx"
)

const maxBodyBytes = 64 << 10

// decodeJSON reads one JSON object, rejecting unknown fields, trailing data and oversized bodies.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return tooLargeError{}
		}
		return domain.Invalid("Request body must be a valid JSON object with known fields")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return domain.Invalid("Request body must contain a single JSON object")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) { httpx.WriteJSON(w, status, v) }

func noContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

// ---- DTOs (snake_case wire format) ---------------------------------------------------------

type userDTO struct {
	ID          string     `json:"id"`
	Email       string     `json:"email,omitempty"`
	DisplayName string     `json:"display_name"`
	CreatedAt   *time.Time `json:"created_at,omitempty"`
}

func toUser(u domain.User) userDTO {
	d := userDTO{ID: u.ID, Email: u.Email, DisplayName: u.DisplayName}
	if !u.CreatedAt.IsZero() {
		t := u.CreatedAt
		d.CreatedAt = &t
	}
	return d
}

type deviceDTO struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Platform   string     `json:"platform"`
	LastSeenAt *time.Time `json:"last_seen_at"`
	CreatedAt  time.Time  `json:"created_at"`
	Current    bool       `json:"current"`
}

type sessionDTO struct {
	ID              string    `json:"id"`
	DeviceID        string    `json:"device_id"`
	DeviceName      string    `json:"device_name"`
	Platform        string    `json:"platform"`
	StartedAt       time.Time `json:"started_at"`
	LastRefreshedAt time.Time `json:"last_refreshed_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	Current         bool      `json:"current"`
}

type memberDTO struct {
	UserID      string    `json:"user_id"`
	DisplayName string    `json:"display_name"`
	Role        string    `json:"role"`
	JoinedAt    time.Time `json:"joined_at"`
}

func toMember(m domain.Member) memberDTO {
	return memberDTO{UserID: m.UserID, DisplayName: m.DisplayName, Role: m.Role, JoinedAt: m.JoinedAt}
}

type peerDTO struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
}

// conversationDTO. last_message and unread_count are reserved: they are always null/0 until
// messages exist (Phase 3) and unread tracking exists (Phase 4A).
type conversationDTO struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"`
	Title       *string   `json:"title"`
	CreatedBy   *string   `json:"created_by"`
	MemberCount int       `json:"member_count"`
	Role        string    `json:"role"`
	Peer        *peerDTO  `json:"peer,omitempty"`
	LastSeq     int64     `json:"last_seq"`
	LastMessage any       `json:"last_message"`
	UnreadCount int       `json:"unread_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type lastMessageDTO struct {
	ID        string    `json:"id"`
	Seq       int64     `json:"seq"`
	SenderID  *string   `json:"sender_id"`
	Body      *string   `json:"body"` // truncated preview; null when deleted
	Deleted   bool      `json:"deleted"`
	CreatedAt time.Time `json:"created_at"`
}

func toConversation(c domain.Conversation, memberCount int, role string, peer *domain.Peer, last *domain.MessagePreview) conversationDTO {
	d := conversationDTO{
		ID: c.ID, Type: c.Type, Title: c.Title, CreatedBy: c.CreatedBy, LastSeq: c.LastSeq,
		MemberCount: memberCount, Role: role, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
	if peer != nil {
		d.Peer = &peerDTO{UserID: peer.UserID, DisplayName: peer.DisplayName}
	}
	if last != nil {
		d.LastMessage = lastMessageDTO{ID: last.ID, Seq: last.Seq, SenderID: last.SenderID, Body: last.Body, Deleted: last.Deleted, CreatedAt: last.CreatedAt}
	}
	return d
}

// ---- pagination cursor ---------------------------------------------------------------------

type cursorJSON struct {
	T  time.Time `json:"t"`
	ID string    `json:"id"`
}

var cursorIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func encodeCursor(c *domain.ListCursor) *string {
	if c == nil {
		return nil
	}
	b, err := json.Marshal(cursorJSON{T: c.UpdatedAt, ID: c.ID})
	if err != nil {
		return nil
	}
	s := base64.RawURLEncoding.EncodeToString(b)
	return &s
}

func decodeCursor(raw string) (*domain.ListCursor, error) {
	if raw == "" {
		return nil, nil
	}
	bad := domain.Invalid("cursor is not valid")
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, bad
	}
	var c cursorJSON
	if err := json.Unmarshal(b, &c); err != nil || c.T.IsZero() || !cursorIDRe.MatchString(c.ID) {
		return nil, bad
	}
	return &domain.ListCursor{UpdatedAt: c.T, ID: c.ID}, nil
}
