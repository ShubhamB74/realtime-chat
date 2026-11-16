// Package chats implements conversation management and its authorization rules.
//
// Authorization model (enforced here, on the server, for every call):
//   - The caller must be an ACTIVE member of a conversation to see anything about it.
//     Non-members and removed members get NotFound, so existence never leaks.
//   - Group administration (rename, add/remove others, roles, settings) requires the admin role
//     and returns Forbidden to members (who already know the group exists).
//   - Any member may leave a group. Direct conversations have fixed membership.
//   - Client-supplied conversation and user ids are never trusted: each is validated and
//     checked against membership or user state.
package chats

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"realtime-chat/backend/internal/app/accounts"
	"realtime-chat/backend/internal/domain"
)

// MaxGroupMembers is the assumed group size cap (docs/capacity-estimation.md).
const MaxGroupMembers = 256

const (
	DefaultPageSize = 30
	MaxPageSize     = 100
	maxAddBatch     = 100
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// NormalizeID validates a UUID string and lowercases it. Invalid ids are reported as NotFound
// because an id that cannot exist must not be distinguishable from one the caller may not see.
func NormalizeID(id string) (string, error) {
	if !uuidRe.MatchString(id) {
		return "", domain.NotFound()
	}
	return strings.ToLower(id), nil
}

// Repo is the persistence the service needs (implemented by postgres.Store).
type Repo interface {
	GetOrCreateDirect(ctx context.Context, userA, userB string) (domain.Conversation, bool, error)
	CreateGroup(ctx context.Context, creator, title string, memberIDs []string) (domain.Conversation, error)
	Membership(ctx context.Context, convID, userID string) (domain.Member, error)
	GetConversation(ctx context.Context, convID string) (domain.Conversation, error)
	ActiveMemberCount(ctx context.Context, convID string) (int, error)
	ListForUser(ctx context.Context, userID string, after *domain.ListCursor, limit int) ([]domain.ConversationSummary, *domain.ListCursor, error)
	UpdateTitle(ctx context.Context, convID, title string) (domain.Conversation, error)
	ListMembers(ctx context.Context, convID string) ([]domain.Member, error)
	AddMembers(ctx context.Context, convID string, userIDs []string, maxMembers int) ([]string, error)
	RemoveMember(ctx context.Context, convID, userID string) error
	SetMemberRole(ctx context.Context, convID, userID, role string) error
	GetSettings(ctx context.Context, convID string) (domain.Settings, error)
	SetAIEnabled(ctx context.Context, convID string, enabled bool) (domain.Settings, error)
	ActiveUserIDs(ctx context.Context, ids []string) (map[string]bool, error)
}

// Service holds conversation use cases.
type Service struct{ repo Repo }

// New builds the service.
func New(repo Repo) *Service { return &Service{repo: repo} }

// Detail is a conversation as seen by one member.
type Detail struct {
	domain.Conversation
	MemberCount int
	Role        string // the caller's role
}

// authorize returns the caller's membership or NotFound.
func (s *Service) authorize(ctx context.Context, convID, userID string) (string, domain.Member, error) {
	id, err := NormalizeID(convID)
	if err != nil {
		return "", domain.Member{}, err
	}
	m, err := s.repo.Membership(ctx, id, userID)
	if err != nil {
		return "", domain.Member{}, err
	}
	return id, m, nil
}

// authorizeAdmin requires an active admin of a group.
func (s *Service) authorizeAdmin(ctx context.Context, convID, userID string) (string, error) {
	id, m, err := s.authorize(ctx, convID, userID)
	if err != nil {
		return "", err
	}
	if m.Role != domain.RoleAdmin {
		return "", domain.Forbidden("Only group admins can do this")
	}
	return id, nil
}

// OpenDirect returns the direct conversation between caller and target, creating it if needed.
func (s *Service) OpenDirect(ctx context.Context, userID, targetID string) (Detail, bool, error) {
	target, err := NormalizeID(targetID)
	if err != nil {
		return Detail{}, false, domain.Invalid("user_id is not valid")
	}
	if target == userID {
		return Detail{}, false, domain.Invalid("You cannot start a conversation with yourself")
	}
	active, err := s.repo.ActiveUserIDs(ctx, []string{target})
	if err != nil {
		return Detail{}, false, err
	}
	if !active[target] {
		return Detail{}, false, domain.NotFound()
	}
	conv, created, err := s.repo.GetOrCreateDirect(ctx, userID, target)
	if err != nil {
		return Detail{}, false, err
	}
	return Detail{Conversation: conv, MemberCount: 2, Role: domain.RoleMember}, created, nil
}

// uniqueOthers validates, lowercases and de-duplicates ids, dropping the caller.
func uniqueOthers(ids []string, self string) ([]string, error) {
	seen := map[string]bool{self: true}
	var out []string
	for _, raw := range ids {
		id, err := NormalizeID(raw)
		if err != nil {
			return nil, domain.Invalid("One or more user ids are not valid")
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

// requireActive checks that every id belongs to an existing, active user.
func (s *Service) requireActive(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	active, err := s.repo.ActiveUserIDs(ctx, ids)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if !active[id] {
			return domain.Invalid("One or more users do not exist")
		}
	}
	return nil
}

// CreateGroup creates a group; the caller becomes its admin.
func (s *Service) CreateGroup(ctx context.Context, userID, title string, memberIDs []string) (Detail, error) {
	t, err := accounts.CleanText(title, "Title", 100)
	if err != nil {
		return Detail{}, err
	}
	others, err := uniqueOthers(memberIDs, userID)
	if err != nil {
		return Detail{}, err
	}
	if len(others) < 1 {
		return Detail{}, domain.Invalid("A group needs at least one other member")
	}
	if len(others)+1 > MaxGroupMembers {
		return Detail{}, domain.Invalid(fmt.Sprintf("A group can have at most %d members", MaxGroupMembers))
	}
	if err := s.requireActive(ctx, others); err != nil {
		return Detail{}, err
	}
	conv, err := s.repo.CreateGroup(ctx, userID, t, others)
	if err != nil {
		return Detail{}, err
	}
	return Detail{Conversation: conv, MemberCount: len(others) + 1, Role: domain.RoleAdmin}, nil
}

// Get returns a conversation the caller belongs to.
func (s *Service) Get(ctx context.Context, userID, convID string) (Detail, error) {
	id, m, err := s.authorize(ctx, convID, userID)
	if err != nil {
		return Detail{}, err
	}
	conv, err := s.repo.GetConversation(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	n, err := s.repo.ActiveMemberCount(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	return Detail{Conversation: conv, MemberCount: n, Role: m.Role}, nil
}

// List returns the caller's conversations. limit <= 0 uses the default; values above the
// maximum are capped.
func (s *Service) List(ctx context.Context, userID string, after *domain.ListCursor, limit int) ([]domain.ConversationSummary, *domain.ListCursor, error) {
	switch {
	case limit <= 0:
		limit = DefaultPageSize
	case limit > MaxPageSize:
		limit = MaxPageSize
	}
	return s.repo.ListForUser(ctx, userID, after, limit)
}

// Rename changes a group's title (admin only).
func (s *Service) Rename(ctx context.Context, userID, convID, title string) (Detail, error) {
	id, err := s.authorizeAdmin(ctx, convID, userID)
	if err != nil {
		return Detail{}, err
	}
	t, err := accounts.CleanText(title, "Title", 100)
	if err != nil {
		return Detail{}, err
	}
	conv, err := s.repo.GetConversation(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	if conv.Type != domain.ConvGroup {
		return Detail{}, domain.Invalid("Direct conversations have no title")
	}
	updated, err := s.repo.UpdateTitle(ctx, id, t)
	if err != nil {
		return Detail{}, err
	}
	n, err := s.repo.ActiveMemberCount(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	return Detail{Conversation: updated, MemberCount: n, Role: domain.RoleAdmin}, nil
}

// Members lists active members (any member may see who else is in the conversation).
func (s *Service) Members(ctx context.Context, userID, convID string) ([]domain.Member, error) {
	id, _, err := s.authorize(ctx, convID, userID)
	if err != nil {
		return nil, err
	}
	return s.repo.ListMembers(ctx, id)
}

// AddMembers adds users to a group (admin only) and returns the members actually added.
func (s *Service) AddMembers(ctx context.Context, userID, convID string, userIDs []string) ([]domain.Member, error) {
	id, err := s.authorizeAdmin(ctx, convID, userID)
	if err != nil {
		return nil, err
	}
	others, err := uniqueOthers(userIDs, userID)
	if err != nil {
		return nil, err
	}
	if len(others) == 0 || len(others) > maxAddBatch {
		return nil, domain.Invalid(fmt.Sprintf("Provide between 1 and %d users to add", maxAddBatch))
	}
	if err := s.requireActive(ctx, others); err != nil {
		return nil, err
	}
	added, err := s.repo.AddMembers(ctx, id, others, MaxGroupMembers)
	if err != nil {
		return nil, err
	}
	if len(added) == 0 {
		return []domain.Member{}, nil
	}
	all, err := s.repo.ListMembers(ctx, id)
	if err != nil {
		return nil, err
	}
	isAdded := make(map[string]bool, len(added))
	for _, a := range added {
		isAdded[a] = true
	}
	out := make([]domain.Member, 0, len(added))
	for _, m := range all {
		if isAdded[m.UserID] {
			out = append(out, m)
		}
	}
	return out, nil
}

// RemoveMember removes a member. Members may remove themselves (leave); removing someone
// else requires admin.
func (s *Service) RemoveMember(ctx context.Context, userID, convID, targetID string) error {
	id, m, err := s.authorize(ctx, convID, userID)
	if err != nil {
		return err
	}
	target, err := NormalizeID(targetID)
	if err != nil {
		return err
	}
	if target != userID && m.Role != domain.RoleAdmin {
		return domain.Forbidden("Only group admins can remove other members")
	}
	return s.repo.RemoveMember(ctx, id, target)
}

// SetRole changes a member's role (admin only).
func (s *Service) SetRole(ctx context.Context, userID, convID, targetID, role string) error {
	id, err := s.authorizeAdmin(ctx, convID, userID)
	if err != nil {
		return err
	}
	target, err := NormalizeID(targetID)
	if err != nil {
		return err
	}
	if role != domain.RoleAdmin && role != domain.RoleMember {
		return domain.Invalid("Role must be admin or member")
	}
	return s.repo.SetMemberRole(ctx, id, target, role)
}

// Settings returns conversation-level settings to any member.
func (s *Service) Settings(ctx context.Context, userID, convID string) (domain.Settings, error) {
	id, _, err := s.authorize(ctx, convID, userID)
	if err != nil {
		return domain.Settings{}, err
	}
	return s.repo.GetSettings(ctx, id)
}

// SetAIEnabled toggles the conversation-level AI switch (group admins only). Direct
// conversations cannot enable AI until both-party consent exists (ADR-004, Phase 7).
func (s *Service) SetAIEnabled(ctx context.Context, userID, convID string, enabled bool) (domain.Settings, error) {
	id, err := s.authorizeAdmin(ctx, convID, userID)
	if err != nil {
		return domain.Settings{}, err
	}
	conv, err := s.repo.GetConversation(ctx, id)
	if err != nil {
		return domain.Settings{}, err
	}
	if conv.Type != domain.ConvGroup {
		return domain.Settings{}, domain.Invalid("AI settings cannot be changed for direct conversations yet")
	}
	return s.repo.SetAIEnabled(ctx, id, enabled)
}
