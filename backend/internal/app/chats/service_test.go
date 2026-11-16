package chats

import (
	"context"
	"errors"
	"strings"
	"testing"

	"realtime-chat/backend/internal/domain"
)

const (
	alice = "00000000-0000-0000-0000-00000000000a" // admin of conv1
	bob   = "00000000-0000-0000-0000-00000000000b" // member of conv1
	carol = "00000000-0000-0000-0000-00000000000c" // not in conv1
	conv1 = "11111111-1111-1111-1111-111111111111"
	dm1   = "22222222-2222-2222-2222-222222222222"
)

// fakeRepo embeds the interface so only the methods a test needs are implemented; calling any
// other method panics, which also proves the service did not reach the store.
type fakeRepo struct {
	Repo
	roles     map[string]map[string]string // conv -> user -> role (active members)
	convTypes map[string]string
	users     map[string]bool // active users
	removed   []string
	added     []string
}

func newFake() *fakeRepo {
	return &fakeRepo{
		roles:     map[string]map[string]string{conv1: {alice: domain.RoleAdmin, bob: domain.RoleMember}, dm1: {alice: domain.RoleMember, bob: domain.RoleMember}},
		convTypes: map[string]string{conv1: domain.ConvGroup, dm1: domain.ConvDirect},
		users:     map[string]bool{alice: true, bob: true, carol: true},
	}
}

func (f *fakeRepo) Membership(_ context.Context, convID, userID string) (domain.Member, error) {
	if role, ok := f.roles[convID][userID]; ok {
		return domain.Member{UserID: userID, Role: role}, nil
	}
	return domain.Member{}, domain.NotFound()
}
func (f *fakeRepo) GetConversation(_ context.Context, id string) (domain.Conversation, error) {
	title := "Team"
	return domain.Conversation{ID: id, Type: f.convTypes[id], Title: &title}, nil
}
func (f *fakeRepo) ActiveMemberCount(_ context.Context, id string) (int, error) {
	return len(f.roles[id]), nil
}
func (f *fakeRepo) UpdateTitle(_ context.Context, id, title string) (domain.Conversation, error) {
	return domain.Conversation{ID: id, Type: domain.ConvGroup, Title: &title}, nil
}
func (f *fakeRepo) ActiveUserIDs(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		if f.users[id] {
			out[id] = true
		}
	}
	return out, nil
}
func (f *fakeRepo) AddMembers(_ context.Context, _ string, ids []string, _ int) ([]string, error) {
	f.added = ids
	return ids, nil
}
func (f *fakeRepo) ListMembers(_ context.Context, id string) ([]domain.Member, error) {
	var out []domain.Member
	for u, r := range f.roles[id] {
		out = append(out, domain.Member{UserID: u, Role: r})
	}
	return out, nil
}
func (f *fakeRepo) RemoveMember(_ context.Context, _, userID string) error {
	f.removed = append(f.removed, userID)
	return nil
}
func (f *fakeRepo) CreateGroup(_ context.Context, _, title string, ids []string) (domain.Conversation, error) {
	f.added = ids
	return domain.Conversation{ID: "new", Type: domain.ConvGroup, Title: &title}, nil
}
func (f *fakeRepo) GetOrCreateDirect(_ context.Context, a, b string) (domain.Conversation, bool, error) {
	return domain.Conversation{ID: dm1, Type: domain.ConvDirect}, true, nil
}

func svc() (*Service, *fakeRepo) {
	f := newFake()
	return New(f), f
}

func TestGetHidesConversationsFromNonMembers(t *testing.T) {
	s, _ := svc()
	ctx := context.Background()
	if _, err := s.Get(ctx, carol, conv1); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("non-member: want not found, got %v", err)
	}
	if _, err := s.Get(ctx, carol, dm1); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("third party on a direct conversation: want not found, got %v", err)
	}
	if _, err := s.Get(ctx, alice, "not-a-uuid"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("malformed id: want not found, got %v", err)
	}
	d, err := s.Get(ctx, bob, strings.ToUpper(conv1))
	if err != nil || d.Role != domain.RoleMember || d.MemberCount != 2 {
		t.Fatalf("member: detail=%+v err=%v", d, err)
	}
}

func TestEveryOperationRejectsNonMembers(t *testing.T) {
	s, f := svc()
	ctx := context.Background()
	ops := map[string]func() error{
		"members":  func() error { _, err := s.Members(ctx, carol, conv1); return err },
		"rename":   func() error { _, err := s.Rename(ctx, carol, conv1, "x"); return err },
		"add":      func() error { _, err := s.AddMembers(ctx, carol, conv1, []string{bob}); return err },
		"remove":   func() error { return s.RemoveMember(ctx, carol, conv1, bob) },
		"leave":    func() error { return s.RemoveMember(ctx, carol, conv1, carol) },
		"role":     func() error { return s.SetRole(ctx, carol, conv1, bob, domain.RoleAdmin) },
		"settings": func() error { _, err := s.Settings(ctx, carol, conv1); return err },
		"ai":       func() error { _, err := s.SetAIEnabled(ctx, carol, conv1, true); return err },
	}
	for name, op := range ops {
		if err := op(); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s: want not found, got %v", name, err)
		}
	}
	if len(f.removed) != 0 || len(f.added) != 0 {
		t.Error("store must not be modified for non-members")
	}
}

func TestAdminOnlyOperationsForbidMembers(t *testing.T) {
	s, f := svc()
	ctx := context.Background()
	ops := map[string]func() error{
		"rename":       func() error { _, err := s.Rename(ctx, bob, conv1, "x"); return err },
		"add":          func() error { _, err := s.AddMembers(ctx, bob, conv1, []string{carol}); return err },
		"remove other": func() error { return s.RemoveMember(ctx, bob, conv1, alice) },
		"role":         func() error { return s.SetRole(ctx, bob, conv1, bob, domain.RoleAdmin) },
		"ai":           func() error { _, err := s.SetAIEnabled(ctx, bob, conv1, true); return err },
	}
	for name, op := range ops {
		if err := op(); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s: want forbidden, got %v", name, err)
		}
	}
	if len(f.removed) != 0 || len(f.added) != 0 {
		t.Error("store must not be modified when forbidden")
	}
}

func TestMemberMayLeaveAndAdminMayRemove(t *testing.T) {
	s, f := svc()
	ctx := context.Background()
	if err := s.RemoveMember(ctx, bob, conv1, bob); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if err := s.RemoveMember(ctx, alice, conv1, bob); err != nil {
		t.Fatalf("admin remove: %v", err)
	}
	if len(f.removed) != 2 {
		t.Errorf("removed = %v", f.removed)
	}
}

func TestAdminOperations(t *testing.T) {
	s, f := svc()
	ctx := context.Background()
	d, err := s.Rename(ctx, alice, conv1, "  New name ")
	if err != nil || d.Title == nil || *d.Title != "New name" {
		t.Fatalf("rename: %+v %v", d, err)
	}
	if _, err := s.Rename(ctx, alice, conv1, "  "); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("blank title: %v", err)
	}
	if _, err := s.AddMembers(ctx, alice, conv1, []string{carol, strings.ToUpper(carol), alice}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(f.added) != 1 || f.added[0] != carol {
		t.Errorf("ids should be lowercased, de-duplicated and exclude the caller: %v", f.added)
	}
	if _, err := s.AddMembers(ctx, alice, conv1, []string{"00000000-0000-0000-0000-0000000000ff"}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("unknown user: %v", err)
	}
	if _, err := s.AddMembers(ctx, alice, conv1, []string{"bad"}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("bad id: %v", err)
	}
	if _, err := s.AddMembers(ctx, alice, conv1, nil); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("empty list: %v", err)
	}
	if err := s.SetRole(ctx, alice, conv1, bob, "owner"); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("bad role: %v", err)
	}
}

func TestDirectConversationRules(t *testing.T) {
	s, _ := svc()
	ctx := context.Background()
	if _, _, err := s.OpenDirect(ctx, alice, alice); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("self: %v", err)
	}
	if _, _, err := s.OpenDirect(ctx, alice, "00000000-0000-0000-0000-0000000000ff"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown target: %v", err)
	}
	if _, _, err := s.OpenDirect(ctx, alice, "bad"); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("bad id: %v", err)
	}
	d, created, err := s.OpenDirect(ctx, alice, carol)
	if err != nil || !created || d.MemberCount != 2 {
		t.Errorf("open: %+v created=%v err=%v", d, created, err)
	}
	if _, err := s.Rename(ctx, alice, dm1, "x"); !errors.Is(err, domain.ErrForbidden) && !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("renaming a direct conversation must fail, got %v", err)
	}
}

func TestCreateGroupRules(t *testing.T) {
	s, f := svc()
	ctx := context.Background()
	if _, err := s.CreateGroup(ctx, alice, "Team", nil); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("no members: %v", err)
	}
	if _, err := s.CreateGroup(ctx, alice, "Team", []string{alice}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("only self: %v", err)
	}
	if _, err := s.CreateGroup(ctx, alice, "", []string{bob}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("no title: %v", err)
	}
	many := make([]string, 0, MaxGroupMembers+1)
	for i := 0; i < MaxGroupMembers+1; i++ {
		many = append(many, "00000000-0000-0000-0001-"+strings.Repeat("0", 8)+hex4(i))
	}
	if _, err := s.CreateGroup(ctx, alice, "Big", many); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("too many members: %v", err)
	}
	d, err := s.CreateGroup(ctx, alice, "Team", []string{bob, bob, alice, carol})
	if err != nil || d.Role != domain.RoleAdmin || d.MemberCount != 3 || len(f.added) != 2 {
		t.Fatalf("create: %+v err=%v added=%v", d, err, f.added)
	}
}

func hex4(i int) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[(i>>12)&15], digits[(i>>8)&15], digits[(i>>4)&15], digits[i&15]})
}

func TestListClampsLimit(t *testing.T) {
	var got int
	s := New(&listRepo{limit: &got})
	_, _, _ = s.List(context.Background(), alice, nil, 0)
	if got != DefaultPageSize {
		t.Errorf("default limit = %d", got)
	}
	_, _, _ = s.List(context.Background(), alice, nil, 100000)
	if got != MaxPageSize {
		t.Errorf("capped limit = %d", got)
	}
}

type listRepo struct {
	Repo
	limit *int
}

func (l *listRepo) ListForUser(_ context.Context, _ string, _ *domain.ListCursor, limit int) ([]domain.ConversationSummary, *domain.ListCursor, error) {
	*l.limit = limit
	return nil, nil, nil
}
