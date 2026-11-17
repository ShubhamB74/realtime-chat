// Package domain holds core types and error kinds. It imports nothing from the rest of the
// application, so business logic stays independent of infrastructure.
package domain

import (
	"errors"
	"time"
)

// Error kinds. Handlers map these to HTTP status codes; use errors.Is.
var (
	ErrInvalid         = errors.New("invalid")
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrForbidden       = errors.New("forbidden")
	ErrNotFound        = errors.New("not found")
	ErrConflict        = errors.New("conflict")
)

// Error carries a client-safe message and a kind. Messages must never contain internals,
// secrets, or message content.
type Error struct {
	Kind error
	Msg  string
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) Unwrap() error { return e.Kind }

func Invalid(msg string) error         { return &Error{Kind: ErrInvalid, Msg: msg} }
func Conflict(msg string) error        { return &Error{Kind: ErrConflict, Msg: msg} }
func Forbidden(msg string) error       { return &Error{Kind: ErrForbidden, Msg: msg} }
func NotFound() error                  { return &Error{Kind: ErrNotFound, Msg: "Resource not found"} }
func Unauthenticated(msg string) error { return &Error{Kind: ErrUnauthenticated, Msg: msg} }

// Roles and conversation types.
const (
	RoleAdmin  = "admin"
	RoleMember = "member"

	ConvDirect = "direct"
	ConvGroup  = "group"

	StatusActive   = "active"
	StatusDisabled = "disabled"
)

// Platforms accepted at device registration (mirrors the devices table constraint).
var Platforms = map[string]bool{"web": true, "ios": true, "android": true, "desktop": true, "other": true}

type User struct {
	ID          string
	Email       string
	DisplayName string
	Status      string
	CreatedAt   time.Time
}

type Device struct {
	ID         string
	Name       string
	Platform   string
	LastSeenAt *time.Time
	CreatedAt  time.Time
}

// Session is one refresh-token row. FamilyID is the stable session identity across rotations.
type Session struct {
	ID        string
	UserID    string
	DeviceID  string
	FamilyID  string
	ExpiresAt time.Time
}

// SessionInfo is the user-facing view of a live session.
type SessionInfo struct {
	ID              string // family id
	DeviceID        string
	DeviceName      string
	Platform        string
	StartedAt       time.Time
	LastRefreshedAt time.Time
	ExpiresAt       time.Time
}

type CreateLoginParams struct {
	UserID     string
	DeviceID   string // optional: reuse an existing device
	DeviceName string
	Platform   string
	TokenHash  []byte
	TTL        time.Duration
	MaxDevices int
}

type RotateParams struct {
	OldHash     []byte
	NewHash     []byte
	TTL         time.Duration
	MaxLifetime time.Duration
}

type RotateOutcome int

const (
	RotateOK RotateOutcome = iota
	RotateNotFound
	RotateRevoked
	RotateExpired
	RotateReuseDetected // family has been revoked
)

type RotateResult struct {
	Outcome RotateOutcome
	Session Session // valid when Outcome == RotateOK (the new session row)
}

type Conversation struct {
	ID        string
	Type      string
	Title     *string
	CreatedBy *string
	LastSeq   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Member struct {
	UserID      string
	DisplayName string
	Role        string
	ReadSeq     int64
	JoinedAt    time.Time
}

type Peer struct {
	UserID      string
	DisplayName string
}

type ConversationSummary struct {
	Conversation
	MemberCount int
	Role        string
	Peer        *Peer           // set for direct conversations
	LastMessage *MessagePreview // latest message visible to the member, if any
}

type ListCursor struct {
	UpdatedAt time.Time
	ID        string
}

type Settings struct {
	AIEnabled bool
}

// Message is a stored message. A deleted message is a tombstone: Body is nil and DeletedAt set.
type Message struct {
	ID             string
	ConversationID string
	SenderID       *string
	Seq            int64 // immutable display order within the conversation
	ModSeq         int64 // bumped on every edit/delete; the sync cursor
	Body           *string
	EditedAt       *time.Time
	DeletedAt      *time.Time
	CreatedAt      time.Time
	IdempotencyKey string // set only on results of Send
}

// MessagePreview is the list-view summary of the latest message.
type MessagePreview struct {
	ID        string
	Seq       int64
	SenderID  *string
	Body      *string // truncated; nil when deleted
	Deleted   bool
	CreatedAt time.Time
}

type SendParams struct {
	ConversationID string
	UserID         string
	DeviceID       string
	IdempotencyKey string
	Body           string
}

// MessageResult is the outcome of a message mutation. Recipients are the active members at
// commit time. Duplicate means nothing changed (an idempotent retry or a no-op delete).
type MessageResult struct {
	Message    Message
	Recipients []string
	Duplicate  bool
}

// Changes is one page of a sync response.
type Changes struct {
	Messages []Message
	HasMore  bool
	LastSeq  int64
}
