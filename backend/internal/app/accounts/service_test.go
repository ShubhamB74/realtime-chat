package accounts

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"realtime-chat/backend/internal/auth"
	"realtime-chat/backend/internal/domain"
)

// ---- fakes ---------------------------------------------------------------------------------

type fakeHasher struct{ verifyCalls int }

func (f *fakeHasher) Hash(_ context.Context, pw string) (string, error) { return "hash:" + pw, nil }
func (f *fakeHasher) Verify(_ context.Context, pw, enc string) (bool, bool, error) {
	f.verifyCalls++
	if enc == "legacy:"+pw {
		return true, true, nil // valid but needs rehash
	}
	return enc == "hash:"+pw, false, nil
}

type fakeTokens struct{}

func (fakeTokens) Issue(u, s, d string) (string, error) { return "access:" + u + ":" + s + ":" + d, nil }
func (fakeTokens) TTL() time.Duration                    { return 15 * time.Minute }

type fakeRepo struct {
	users       map[string]domain.User // by email
	hashes      map[string]string      // by email
	createErr   error
	loginParams *domain.CreateLoginParams
	rotateOut   domain.RotateOutcome
	rotateParam domain.RotateParams
	revoked     []string
	rehashed    string
}

func (r *fakeRepo) CreateUser(_ context.Context, email, name, hash string) (domain.User, error) {
	if r.createErr != nil {
		return domain.User{}, r.createErr
	}
	u := domain.User{ID: "u-" + email, Email: email, DisplayName: name, Status: domain.StatusActive}
	r.users[email], r.hashes[email] = u, hash
	return u, nil
}
func (r *fakeRepo) UserByEmail(_ context.Context, email string) (domain.User, string, error) {
	u, ok := r.users[email]
	if !ok {
		return domain.User{}, "", domain.NotFound()
	}
	return u, r.hashes[email], nil
}
func (r *fakeRepo) UserByID(_ context.Context, id string) (domain.User, error) {
	for _, u := range r.users {
		if u.ID == id {
			return u, nil
		}
	}
	return domain.User{}, domain.NotFound()
}
func (r *fakeRepo) UpdateDisplayName(_ context.Context, id, name string) (domain.User, error) {
	return domain.User{ID: id, DisplayName: name}, nil
}
func (r *fakeRepo) UpdatePasswordHash(_ context.Context, id, hash string) error {
	r.rehashed = hash
	return nil
}
func (r *fakeRepo) CreateLoginSession(_ context.Context, p domain.CreateLoginParams) (domain.Device, domain.Session, error) {
	r.loginParams = &p
	return domain.Device{ID: "dev-1"}, domain.Session{ID: "sess-row", FamilyID: "fam-1", UserID: p.UserID, DeviceID: "dev-1"}, nil
}
func (r *fakeRepo) RotateSession(_ context.Context, p domain.RotateParams) (domain.RotateResult, error) {
	r.rotateParam = p
	if r.rotateOut != domain.RotateOK {
		return domain.RotateResult{Outcome: r.rotateOut}, nil
	}
	return domain.RotateResult{Outcome: domain.RotateOK, Session: domain.Session{UserID: "u-a@example.com", DeviceID: "dev-1", FamilyID: "fam-1"}}, nil
}
func (r *fakeRepo) RevokeFamily(_ context.Context, userID, fam string) (bool, error) {
	r.revoked = append(r.revoked, userID+"/"+fam)
	return true, nil
}
func (r *fakeRepo) ListActiveSessions(context.Context, string) ([]domain.SessionInfo, error) { return nil, nil }
func (r *fakeRepo) ListDevices(context.Context, string) ([]domain.Device, error)              { return nil, nil }
func (r *fakeRepo) RevokeDevice(context.Context, string, string) error                         { return nil }

func newService(t *testing.T) (*Service, *fakeRepo, *fakeHasher) {
	t.Helper()
	repo := &fakeRepo{users: map[string]domain.User{}, hashes: map[string]string{}}
	h := &fakeHasher{}
	s, err := New(repo, h, fakeTokens{}, Config{RefreshTTL: time.Hour, SessionMaxLifetime: 24 * time.Hour, MaxDevices: 5},
		slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return s, repo, h
}

func login(email, pw string) LoginInput {
	return LoginInput{Email: email, Password: pw, Device: DeviceInput{Name: "Laptop", Platform: "web"}}
}

// ---- registration --------------------------------------------------------------------------

func TestRegisterValidation(t *testing.T) {
	s, _, _ := newService(t)
	cases := map[string]RegisterInput{
		"bad email":        {Email: "not-an-email", Password: "long-enough-pw", DisplayName: "A"},
		"display-name form": {Email: "Alice <a@example.com>", Password: "long-enough-pw", DisplayName: "A"},
		"short password":   {Email: "a@example.com", Password: "short", DisplayName: "A"},
		"huge password":    {Email: "a@example.com", Password: strings.Repeat("x", 129), DisplayName: "A"},
		"empty name":       {Email: "a@example.com", Password: "long-enough-pw", DisplayName: "  "},
		"control chars":    {Email: "a@example.com", Password: "long-enough-pw", DisplayName: "bad\x07name"},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Register(context.Background(), in); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("want ErrInvalid, got %v", err)
			}
		})
	}
}

func TestRegisterNormalizesEmailAndConflictPropagates(t *testing.T) {
	s, repo, _ := newService(t)
	u, err := s.Register(context.Background(), RegisterInput{Email: "  Alice@Example.COM ", Password: "long-enough-pw", DisplayName: " Alice "})
	if err != nil || u.Email != "alice@example.com" || u.DisplayName != "Alice" {
		t.Fatalf("user=%+v err=%v", u, err)
	}
	repo.createErr = domain.Conflict("Email is already registered")
	if _, err := s.Register(context.Background(), RegisterInput{Email: "b@example.com", Password: "long-enough-pw", DisplayName: "B"}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
}

// ---- login ---------------------------------------------------------------------------------

func registered(t *testing.T, s *Service) {
	t.Helper()
	if _, err := s.Register(context.Background(), RegisterInput{Email: "a@example.com", Password: "correct-password", DisplayName: "A"}); err != nil {
		t.Fatal(err)
	}
}

func TestLoginSuccess(t *testing.T) {
	s, repo, _ := newService(t)
	registered(t, s)
	tok, err := s.Login(context.Background(), login("A@Example.com", "correct-password"))
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "access:u-a@example.com:fam-1:dev-1" || tok.RefreshToken == "" || tok.SessionID != "fam-1" || tok.DeviceID != "dev-1" {
		t.Fatalf("unexpected tokens: %+v", tok)
	}
	if repo.loginParams == nil || len(repo.loginParams.TokenHash) != 32 || repo.loginParams.MaxDevices != 5 {
		t.Fatalf("login params: %+v", repo.loginParams)
	}
	if string(repo.loginParams.TokenHash) != string(auth.HashRefreshToken(tok.RefreshToken)) {
		t.Error("stored hash does not match the issued refresh token")
	}
}

func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	s, _, h := newService(t)
	registered(t, s)

	_, errWrong := s.Login(context.Background(), login("a@example.com", "wrong-password!"))
	before := h.verifyCalls
	_, errUnknown := s.Login(context.Background(), login("nobody@example.com", "whatever-password"))
	if h.verifyCalls != before+1 {
		t.Error("unknown email must still perform a password verification (timing equalization)")
	}
	if !errors.Is(errWrong, domain.ErrUnauthenticated) || !errors.Is(errUnknown, domain.ErrUnauthenticated) {
		t.Fatalf("errors: %v / %v", errWrong, errUnknown)
	}
	if errWrong.Error() != errUnknown.Error() {
		t.Errorf("messages differ: %q vs %q", errWrong, errUnknown)
	}
}

func TestLoginDisabledUserRejected(t *testing.T) {
	s, repo, _ := newService(t)
	registered(t, s)
	u := repo.users["a@example.com"]
	u.Status = domain.StatusDisabled
	repo.users["a@example.com"] = u
	if _, err := s.Login(context.Background(), login("a@example.com", "correct-password")); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("want unauthenticated, got %v", err)
	}
}

func TestLoginValidatesDevice(t *testing.T) {
	s, _, _ := newService(t)
	registered(t, s)
	in := login("a@example.com", "correct-password")
	in.Device.Platform = "toaster"
	if _, err := s.Login(context.Background(), in); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want invalid platform, got %v", err)
	}
	in = login("a@example.com", "correct-password")
	in.Device.Name = ""
	if _, err := s.Login(context.Background(), in); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want invalid device name, got %v", err)
	}
}

func TestLoginUpgradesPasswordHashWhenParamsChanged(t *testing.T) {
	s, repo, _ := newService(t)
	registered(t, s)
	repo.hashes["a@example.com"] = "legacy:correct-password"
	if _, err := s.Login(context.Background(), login("a@example.com", "correct-password")); err != nil {
		t.Fatal(err)
	}
	if repo.rehashed != "hash:correct-password" {
		t.Errorf("rehash not stored: %q", repo.rehashed)
	}
}

// ---- refresh -------------------------------------------------------------------------------

func TestRefreshRotates(t *testing.T) {
	s, repo, _ := newService(t)
	registered(t, s)
	old, _, _ := auth.NewRefreshToken()
	tok, err := s.Refresh(context.Background(), old)
	if err != nil {
		t.Fatal(err)
	}
	if tok.RefreshToken == old || tok.RefreshToken == "" {
		t.Error("refresh token must change on rotation")
	}
	if string(repo.rotateParam.OldHash) != string(auth.HashRefreshToken(old)) || string(repo.rotateParam.NewHash) != string(auth.HashRefreshToken(tok.RefreshToken)) {
		t.Error("rotation hashes do not match the tokens")
	}
	if tok.AccessToken != "access:u-a@example.com:fam-1:dev-1" {
		t.Errorf("access token: %s", tok.AccessToken)
	}
}

func TestRefreshRejectsBadOutcomes(t *testing.T) {
	for name, outcome := range map[string]domain.RotateOutcome{
		"unknown": domain.RotateNotFound, "revoked": domain.RotateRevoked,
		"expired": domain.RotateExpired, "reuse": domain.RotateReuseDetected,
	} {
		t.Run(name, func(t *testing.T) {
			s, repo, _ := newService(t)
			registered(t, s)
			repo.rotateOut = outcome
			tok, _, _ := auth.NewRefreshToken()
			if _, err := s.Refresh(context.Background(), tok); !errors.Is(err, domain.ErrUnauthenticated) {
				t.Fatalf("want unauthenticated, got %v", err)
			}
		})
	}
}

func TestRefreshRejectsMalformedTokenWithoutTouchingStore(t *testing.T) {
	s, repo, _ := newService(t)
	repo.rotateOut = domain.RotateOK
	for _, bad := range []string{"", "short", strings.Repeat("a", 500)} {
		if _, err := s.Refresh(context.Background(), bad); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if len(repo.rotateParam.OldHash) != 0 {
		t.Error("store must not be called for malformed tokens")
	}
}

func TestRefreshRevokesSessionOfDisabledUser(t *testing.T) {
	s, repo, _ := newService(t)
	registered(t, s)
	u := repo.users["a@example.com"]
	u.Status = domain.StatusDisabled
	repo.users["a@example.com"] = u
	tok, _, _ := auth.NewRefreshToken()
	if _, err := s.Refresh(context.Background(), tok); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("want unauthenticated, got %v", err)
	}
	if len(repo.revoked) != 1 {
		t.Errorf("session family should be revoked, got %v", repo.revoked)
	}
}

func TestLogoutRevokesFamily(t *testing.T) {
	s, repo, _ := newService(t)
	if err := s.Logout(context.Background(), "u1", "fam-9"); err != nil {
		t.Fatal(err)
	}
	if len(repo.revoked) != 1 || repo.revoked[0] != "u1/fam-9" {
		t.Errorf("revoked = %v", repo.revoked)
	}
}

func TestLookupByEmailExposesMinimalFields(t *testing.T) {
	s, _, _ := newService(t)
	registered(t, s)
	u, err := s.LookupByEmail(context.Background(), "A@example.com")
	if err != nil || u.ID == "" || u.DisplayName != "A" || u.Email != "" {
		t.Fatalf("user=%+v err=%v", u, err)
	}
	if _, err := s.LookupByEmail(context.Background(), "garbage"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("malformed email should be not found, got %v", err)
	}
}
