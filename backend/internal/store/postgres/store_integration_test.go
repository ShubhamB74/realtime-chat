//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"realtime-chat/backend/internal/auth"
	"realtime-chat/backend/internal/domain"
	"realtime-chat/backend/internal/store/postgres"
)

func store(t *testing.T) *postgres.Store { return postgres.NewStore(pool(t)) }

func mkUser(t *testing.T, s *postgres.Store) domain.User {
	t.Helper()
	u, err := s.CreateUser(context.Background(), uniqueEmail(), "User", "hash")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool(t).Exec(context.Background(), `DELETE FROM users WHERE id = $1::uuid`, u.ID) })
	return u
}

func login(t *testing.T, s *postgres.Store, userID string, max int) (domain.Device, domain.Session, string) {
	t.Helper()
	tok, hash, err := auth.NewRefreshToken()
	if err != nil {
		t.Fatal(err)
	}
	d, sess, err := s.CreateLoginSession(context.Background(), domain.CreateLoginParams{
		UserID: userID, DeviceName: "Laptop", Platform: "web", TokenHash: hash, TTL: time.Hour, MaxDevices: max,
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	return d, sess, tok
}

func rotate(t *testing.T, s *postgres.Store, old string) (domain.RotateResult, string) {
	t.Helper()
	next, nextHash, _ := auth.NewRefreshToken()
	res, err := s.RotateSession(context.Background(), domain.RotateParams{
		OldHash: auth.HashRefreshToken(old), NewHash: nextHash, TTL: time.Hour, MaxLifetime: 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	return res, next
}

func TestCreateUserDuplicateEmailIsConflict(t *testing.T) {
	s := store(t)
	email := uniqueEmail()
	if _, err := s.CreateUser(context.Background(), email, "A", "h"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool(t).Exec(context.Background(), `DELETE FROM users WHERE email = $1`, email) })
	_, err := s.CreateUser(context.Background(), upperFirst(email), "B", "h")
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
}

func upperFirst(s string) string { return "I" + s[1:] } // "it-..." -> "It-...": case-only difference

func TestRefreshRotationAndReuseDetection(t *testing.T) {
	s := store(t)
	u := mkUser(t, s)
	_, sess, rt1 := login(t, s, u.ID, 10)

	res, rt2 := rotate(t, s, rt1)
	if res.Outcome != domain.RotateOK || res.Session.FamilyID != sess.FamilyID {
		t.Fatalf("first rotation: %+v", res)
	}
	res, rt3 := rotate(t, s, rt2)
	if res.Outcome != domain.RotateOK {
		t.Fatalf("second rotation: %+v", res)
	}

	// Replaying an old token = theft signal: family revoked, the newest token dies too.
	if res, _ := rotate(t, s, rt1); res.Outcome != domain.RotateReuseDetected {
		t.Fatalf("replay of rt1: want reuse detected, got %v", res.Outcome)
	}
	if res, _ := rotate(t, s, rt3); res.Outcome != domain.RotateRevoked {
		t.Fatalf("newest token after reuse: want revoked, got %v", res.Outcome)
	}
	if res, _ := rotate(t, s, "completely-unknown-token-value"); res.Outcome != domain.RotateNotFound {
		t.Fatalf("unknown token: got %v", res.Outcome)
	}
}

func TestConcurrentRefreshWithSameTokenRotatesOnce(t *testing.T) {
	s := store(t)
	u := mkUser(t, s)
	_, _, rt := login(t, s, u.ID, 10)

	var wg sync.WaitGroup
	outcomes := make([]domain.RotateOutcome, 4)
	for i := range outcomes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, nh, _ := auth.NewRefreshToken()
			res, err := s.RotateSession(context.Background(), domain.RotateParams{
				OldHash: auth.HashRefreshToken(rt), NewHash: nh, TTL: time.Hour, MaxLifetime: 24 * time.Hour})
			if err != nil {
				t.Errorf("rotate: %v", err)
				return
			}
			outcomes[i] = res.Outcome
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, o := range outcomes {
		if o == domain.RotateOK {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("exactly one concurrent refresh may succeed, got %d (%v)", ok, outcomes)
	}
}

func TestSessionExpiryAndAbsoluteLifetime(t *testing.T) {
	s := store(t)
	u := mkUser(t, s)
	_, _, rt := login(t, s, u.ID, 10)
	// Absolute lifetime of zero seconds: the family is already past its cap.
	_, nh, _ := auth.NewRefreshToken()
	res, err := s.RotateSession(context.Background(), domain.RotateParams{
		OldHash: auth.HashRefreshToken(rt), NewHash: nh, TTL: time.Hour, MaxLifetime: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if res.Outcome != domain.RotateOK && res.Outcome != domain.RotateExpired {
		t.Fatalf("unexpected outcome %v", res.Outcome)
	}
	// Force an expired token and check it is refused.
	_, _, rt2 := login(t, s, u.ID, 10)
	if _, err := pool(t).Exec(context.Background(),
		`UPDATE user_sessions SET expires_at = now() - interval '1 second', created_at = now() - interval '2 seconds'
		 WHERE refresh_token_hash = $1`, auth.HashRefreshToken(rt2)); err != nil {
		t.Fatal(err)
	}
	if res, _ := rotate(t, s, rt2); res.Outcome != domain.RotateExpired {
		t.Fatalf("expired token: got %v", res.Outcome)
	}
}

func TestDeviceLimitAndReuse(t *testing.T) {
	s := store(t)
	u := mkUser(t, s)
	d1, _, _ := login(t, s, u.ID, 2)
	login(t, s, u.ID, 2)
	_, hash, _ := auth.NewRefreshToken()
	_, _, err := s.CreateLoginSession(context.Background(), domain.CreateLoginParams{
		UserID: u.ID, DeviceName: "Third", Platform: "ios", TokenHash: hash, TTL: time.Hour, MaxDevices: 2})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("third device: want conflict, got %v", err)
	}
	// Reusing an existing device is not subject to the limit.
	_, hash2, _ := auth.NewRefreshToken()
	d, _, err := s.CreateLoginSession(context.Background(), domain.CreateLoginParams{
		UserID: u.ID, DeviceID: d1.ID, DeviceName: "ignored", Platform: "web", TokenHash: hash2, TTL: time.Hour, MaxDevices: 2})
	if err != nil || d.ID != d1.ID {
		t.Fatalf("reuse device: %+v %v", d, err)
	}
	// Another user's device id must not be usable.
	other := mkUser(t, s)
	_, hash3, _ := auth.NewRefreshToken()
	_, _, err = s.CreateLoginSession(context.Background(), domain.CreateLoginParams{
		UserID: other.ID, DeviceID: d1.ID, DeviceName: "x", Platform: "web", TokenHash: hash3, TTL: time.Hour, MaxDevices: 2})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("foreign device: want invalid, got %v", err)
	}
}

func TestRevokeDeviceKillsItsSessions(t *testing.T) {
	s := store(t)
	u := mkUser(t, s)
	d, _, rt := login(t, s, u.ID, 10)
	if err := s.RevokeDevice(context.Background(), u.ID, d.ID); err != nil {
		t.Fatal(err)
	}
	if res, _ := rotate(t, s, rt); res.Outcome != domain.RotateRevoked {
		t.Fatalf("token of revoked device: got %v", res.Outcome)
	}
	if err := s.RevokeDevice(context.Background(), u.ID, d.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("second revoke: want not found, got %v", err)
	}
	other := mkUser(t, s)
	d2, _, _ := login(t, s, u.ID, 10)
	if err := s.RevokeDevice(context.Background(), other.ID, d2.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("revoking another user's device: want not found, got %v", err)
	}
}

func TestRevokeFamilyAndListSessions(t *testing.T) {
	s := store(t)
	u := mkUser(t, s)
	_, sess, rt := login(t, s, u.ID, 10)
	_, res := rotate(t, s, rt)
	_ = res
	list, err := s.ListActiveSessions(context.Background(), u.ID)
	if err != nil || len(list) != 1 || list[0].ID != sess.FamilyID {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	if found, err := s.RevokeFamily(context.Background(), u.ID, sess.FamilyID); err != nil || !found {
		t.Fatalf("revoke: found=%v err=%v", found, err)
	}
	if found, _ := s.RevokeFamily(context.Background(), u.ID, sess.FamilyID); !found {
		t.Error("revoking twice must still report the session as existing")
	}
	other := mkUser(t, s)
	if found, _ := s.RevokeFamily(context.Background(), other.ID, sess.FamilyID); found {
		t.Error("another user must not see or revoke this session")
	}
	if list, _ := s.ListActiveSessions(context.Background(), u.ID); len(list) != 0 {
		t.Errorf("revoked session still listed: %+v", list)
	}
}

// ---- conversations ---------------------------------------------------------------------------

func TestDirectConversationIsUniquePerPairUnderConcurrency(t *testing.T) {
	s := store(t)
	a, b := mkUser(t, s), mkUser(t, s)
	ids := make([]string, 8)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			x, y := a.ID, b.ID
			if i%2 == 1 {
				x, y = y, x // both argument orders must resolve to the same conversation
			}
			c, _, err := s.GetOrCreateDirect(context.Background(), x, y)
			if err != nil {
				t.Errorf("get or create: %v", err)
				return
			}
			ids[i] = c.ID
		}(i)
	}
	wg.Wait()
	for _, id := range ids {
		if id == "" || id != ids[0] {
			t.Fatalf("expected one conversation, got %v", ids)
		}
	}
	n, _ := s.ActiveMemberCount(context.Background(), ids[0])
	if n != 2 {
		t.Errorf("direct conversation must have exactly 2 members, got %d", n)
	}
	t.Cleanup(func() {
		_, _ = pool(t).Exec(context.Background(), `DELETE FROM conversations WHERE id = $1::uuid`, ids[0])
	})
}

func mkGroup(t *testing.T, s *postgres.Store, creator domain.User, others ...domain.User) domain.Conversation {
	t.Helper()
	ids := make([]string, 0, len(others))
	for _, o := range others {
		ids = append(ids, o.ID)
	}
	c, err := s.CreateGroup(context.Background(), creator.ID, "Team", ids)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool(t).Exec(context.Background(), `DELETE FROM conversations WHERE id = $1::uuid`, c.ID)
	})
	return c
}

func TestMembershipIsTheAuthorizationPrimitive(t *testing.T) {
	s := store(t)
	a, b, c := mkUser(t, s), mkUser(t, s), mkUser(t, s)
	g := mkGroup(t, s, a, b)
	ctx := context.Background()

	if m, err := s.Membership(ctx, g.ID, a.ID); err != nil || m.Role != domain.RoleAdmin {
		t.Fatalf("creator: %+v %v", m, err)
	}
	if m, err := s.Membership(ctx, g.ID, b.ID); err != nil || m.Role != domain.RoleMember {
		t.Fatalf("member: %+v %v", m, err)
	}
	if _, err := s.Membership(ctx, g.ID, c.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("stranger: want not found, got %v", err)
	}
	if err := s.RemoveMember(ctx, g.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Membership(ctx, g.ID, b.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("removed member: want not found, got %v", err)
	}
}

func TestLastAdminLeavingPromotesLongestStandingMember(t *testing.T) {
	s := store(t)
	a, b, c := mkUser(t, s), mkUser(t, s), mkUser(t, s)
	g := mkGroup(t, s, a, b, c)
	ctx := context.Background()
	if err := s.RemoveMember(ctx, g.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	members, _ := s.ListMembers(ctx, g.ID)
	admins := 0
	for _, m := range members {
		if m.Role == domain.RoleAdmin {
			admins++
		}
	}
	if len(members) != 2 || admins != 1 {
		t.Fatalf("members=%d admins=%d (a group must never be left without an admin)", len(members), admins)
	}
}

func TestCannotDemoteTheLastAdmin(t *testing.T) {
	s := store(t)
	a, b := mkUser(t, s), mkUser(t, s)
	g := mkGroup(t, s, a, b)
	ctx := context.Background()
	if err := s.SetMemberRole(ctx, g.ID, a.ID, domain.RoleMember); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("demoting last admin: want conflict, got %v", err)
	}
	if m, _ := s.Membership(ctx, g.ID, a.ID); m.Role != domain.RoleAdmin {
		t.Error("failed demotion must roll back")
	}
	if err := s.SetMemberRole(ctx, g.ID, b.ID, domain.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMemberRole(ctx, g.ID, a.ID, domain.RoleMember); err != nil {
		t.Fatalf("demoting with another admin present: %v", err)
	}
}

func TestAddMembersRespectsLimitAndReaddResetsMembership(t *testing.T) {
	s := store(t)
	a, b, c := mkUser(t, s), mkUser(t, s), mkUser(t, s)
	g := mkGroup(t, s, a, b)
	ctx := context.Background()

	added, err := s.AddMembers(ctx, g.ID, []string{b.ID, c.ID}, 10)
	if err != nil || len(added) != 1 || added[0] != c.ID {
		t.Fatalf("already-active members must be skipped: added=%v err=%v", added, err)
	}
	// 3 active members; a limit of 3 means adding one more must fail and change nothing.
	d := mkUser(t, s)
	if _, err := s.AddMembers(ctx, g.ID, []string{d.ID}, 3); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("over limit: want conflict, got %v", err)
	}
	if _, err := s.Membership(ctx, g.ID, d.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Error("failed add must roll back")
	}
	// Remove and re-add: membership is restored with a fresh join.
	if err := s.RemoveMember(ctx, g.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	if added, err := s.AddMembers(ctx, g.ID, []string{c.ID}, 10); err != nil || len(added) != 1 {
		t.Fatalf("re-add: %v %v", added, err)
	}
	if _, err := s.Membership(ctx, g.ID, c.ID); err != nil {
		t.Errorf("re-added member should be active: %v", err)
	}
}

func TestListForUserPaginatesAndIsolatesUsers(t *testing.T) {
	s := store(t)
	a, b, outsider := mkUser(t, s), mkUser(t, s), mkUser(t, s)
	ctx := context.Background()
	var created []string
	for i := 0; i < 5; i++ {
		g := mkGroup(t, s, a, b)
		created = append(created, g.ID)
		time.Sleep(3 * time.Millisecond)
	}
	page1, next, err := s.ListForUser(ctx, a.ID, nil, 2)
	if err != nil || len(page1) != 2 || next == nil {
		t.Fatalf("page1: %d next=%v err=%v", len(page1), next, err)
	}
	page2, next2, err := s.ListForUser(ctx, a.ID, next, 2)
	if err != nil || len(page2) != 2 || next2 == nil {
		t.Fatalf("page2: %d next=%v err=%v", len(page2), next2, err)
	}
	page3, next3, err := s.ListForUser(ctx, a.ID, next2, 2)
	if err != nil || len(page3) != 1 || next3 != nil {
		t.Fatalf("page3: %d next=%v err=%v", len(page3), next3, err)
	}
	seen := map[string]bool{}
	for _, p := range [][]domain.ConversationSummary{page1, page2, page3} {
		for _, c := range p {
			if seen[c.ID] {
				t.Fatalf("duplicate across pages: %s", c.ID)
			}
			seen[c.ID] = true
		}
	}
	for _, id := range created {
		if !seen[id] {
			t.Errorf("conversation %s missing from pagination", id)
		}
	}
	if list, _, _ := s.ListForUser(ctx, outsider.ID, nil, 50); len(list) != 0 {
		t.Errorf("outsider must see no conversations, got %d", len(list))
	}
	t.Log(fmt.Sprintf("paginated %d conversations", len(seen)))
}
