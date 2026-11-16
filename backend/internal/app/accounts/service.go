// Package accounts implements registration, login, token refresh, sessions, devices and profiles.
package accounts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"realtime-chat/backend/internal/auth"
	"realtime-chat/backend/internal/domain"
)

// Repo is the persistence the service needs (implemented by postgres.Store).
type Repo interface {
	CreateUser(ctx context.Context, email, displayName, passwordHash string) (domain.User, error)
	UserByEmail(ctx context.Context, email string) (domain.User, string, error)
	UserByID(ctx context.Context, id string) (domain.User, error)
	UpdateDisplayName(ctx context.Context, id, name string) (domain.User, error)
	UpdatePasswordHash(ctx context.Context, id, hash string) error
	CreateLoginSession(ctx context.Context, p domain.CreateLoginParams) (domain.Device, domain.Session, error)
	RotateSession(ctx context.Context, p domain.RotateParams) (domain.RotateResult, error)
	RevokeFamily(ctx context.Context, userID, familyID string) (bool, error)
	ListActiveSessions(ctx context.Context, userID string) ([]domain.SessionInfo, error)
	ListDevices(ctx context.Context, userID string) ([]domain.Device, error)
	RevokeDevice(ctx context.Context, userID, deviceID string) error
}

// PasswordHasher abstracts auth.Hasher so tests can use a fast fake.
type PasswordHasher interface {
	Hash(ctx context.Context, password string) (string, error)
	Verify(ctx context.Context, password, encoded string) (ok, needsRehash bool, err error)
}

// TokenIssuer abstracts access-token creation.
type TokenIssuer interface {
	Issue(userID, sessionID, deviceID string) (string, error)
	TTL() time.Duration
}

// Config holds session policy.
type Config struct {
	RefreshTTL         time.Duration
	SessionMaxLifetime time.Duration
	MaxDevices         int
}

// Service holds account use cases.
type Service struct {
	repo      Repo
	hasher    PasswordHasher
	tokens    TokenIssuer
	cfg       Config
	log       *slog.Logger
	dummyHash string // verified against when the email is unknown, to equalize timing
}

// New builds the service and precomputes the dummy hash (one hash operation).
func New(repo Repo, hasher PasswordHasher, tokens TokenIssuer, cfg Config, log *slog.Logger) (*Service, error) {
	dummy, err := hasher.Hash(context.Background(), "dummy-password-for-timing-equalization")
	if err != nil {
		return nil, fmt.Errorf("prepare dummy hash: %w", err)
	}
	return &Service{repo: repo, hasher: hasher, tokens: tokens, cfg: cfg, log: log, dummyHash: dummy}, nil
}

// Tokens is the credential bundle returned by Login and Refresh.
type Tokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    time.Duration
	SessionID    string
	DeviceID     string
	User         domain.User
}

type RegisterInput struct{ Email, Password, DisplayName string }

type DeviceInput struct {
	ID       string // optional existing device
	Name     string
	Platform string
}

type LoginInput struct {
	Email, Password string
	Device          DeviceInput
}

const (
	minPasswordRunes = 10
	maxPasswordRunes = 128
)

// NormalizeEmail trims and lowercases an email address and validates its shape.
func NormalizeEmail(raw string) (string, error) {
	e := strings.ToLower(strings.TrimSpace(raw))
	if len(e) < 3 || len(e) > 320 {
		return "", domain.Invalid("Email address is not valid")
	}
	addr, err := mail.ParseAddress(e)
	if err != nil || addr.Address != e || strings.ContainsAny(e, " <>\"") {
		return "", domain.Invalid("Email address is not valid")
	}
	return e, nil
}

func validatePassword(p string) error {
	n := utf8.RuneCountInString(p)
	if n < minPasswordRunes || n > maxPasswordRunes {
		return domain.Invalid(fmt.Sprintf("Password must be between %d and %d characters", minPasswordRunes, maxPasswordRunes))
	}
	if strings.ContainsRune(p, 0) {
		return domain.Invalid("Password contains invalid characters")
	}
	return nil
}

// CleanText trims and validates a short human-readable string (display names, device names).
func CleanText(s, field string, maxRunes int) (string, error) {
	s = strings.TrimSpace(s)
	n := utf8.RuneCountInString(s)
	if n < 1 || n > maxRunes {
		return "", domain.Invalid(fmt.Sprintf("%s must be between 1 and %d characters", field, maxRunes))
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", domain.Invalid(field + " contains invalid characters")
		}
	}
	return s, nil
}

// Register creates an account. It does not log the user in.
func (s *Service) Register(ctx context.Context, in RegisterInput) (domain.User, error) {
	email, err := NormalizeEmail(in.Email)
	if err != nil {
		return domain.User{}, err
	}
	if err := validatePassword(in.Password); err != nil {
		return domain.User{}, err
	}
	name, err := CleanText(in.DisplayName, "Display name", 100)
	if err != nil {
		return domain.User{}, err
	}
	hash, err := s.hasher.Hash(ctx, in.Password)
	if err != nil {
		return domain.User{}, fmt.Errorf("hash password: %w", err)
	}
	return s.repo.CreateUser(ctx, email, name, hash)
}

var errBadCredentials = domain.Unauthenticated("Invalid credentials")

// Login verifies credentials, registers or reuses the device, and starts a session.
func (s *Service) Login(ctx context.Context, in LoginInput) (Tokens, error) {
	email, err := NormalizeEmail(in.Email)
	if err != nil {
		return Tokens{}, errBadCredentials
	}
	devName, err := CleanText(in.Device.Name, "Device name", 100)
	if err != nil {
		return Tokens{}, err
	}
	if !domain.Platforms[in.Device.Platform] {
		return Tokens{}, domain.Invalid("Device platform is not supported")
	}
	if len(in.Password) == 0 || utf8.RuneCountInString(in.Password) > maxPasswordRunes {
		return Tokens{}, errBadCredentials
	}

	user, hash, err := s.repo.UserByEmail(ctx, email)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		_, _, _ = s.hasher.Verify(ctx, in.Password, s.dummyHash) // equalize timing
		return Tokens{}, errBadCredentials
	case err != nil:
		return Tokens{}, fmt.Errorf("lookup user: %w", err)
	}

	ok, rehash, err := s.hasher.Verify(ctx, in.Password, hash)
	if err != nil {
		if errors.Is(err, auth.ErrMalformedHash) {
			s.log.Error("stored password hash is malformed", slog.String("user_id", user.ID))
			return Tokens{}, errBadCredentials
		}
		return Tokens{}, fmt.Errorf("verify password: %w", err)
	}
	if !ok || user.Status != domain.StatusActive {
		return Tokens{}, errBadCredentials
	}
	if rehash {
		if newHash, err := s.hasher.Hash(ctx, in.Password); err == nil {
			if err := s.repo.UpdatePasswordHash(ctx, user.ID, newHash); err != nil {
				s.log.Warn("password rehash not stored", slog.String("user_id", user.ID))
			}
		}
	}

	refresh, refreshHash, err := auth.NewRefreshToken()
	if err != nil {
		return Tokens{}, err
	}
	dev, sess, err := s.repo.CreateLoginSession(ctx, domain.CreateLoginParams{
		UserID:     user.ID,
		DeviceID:   in.Device.ID,
		DeviceName: devName,
		Platform:   in.Device.Platform,
		TokenHash:  refreshHash,
		TTL:        s.cfg.RefreshTTL,
		MaxDevices: s.cfg.MaxDevices,
	})
	if err != nil {
		return Tokens{}, err
	}
	access, err := s.tokens.Issue(user.ID, sess.FamilyID, dev.ID)
	if err != nil {
		return Tokens{}, fmt.Errorf("issue access token: %w", err)
	}
	return Tokens{AccessToken: access, RefreshToken: refresh, ExpiresIn: s.tokens.TTL(), SessionID: sess.FamilyID, DeviceID: dev.ID, User: user}, nil
}

var errBadRefresh = domain.Unauthenticated("Invalid refresh token")

// Refresh rotates a refresh token. Reuse of an already-rotated token revokes the session.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (Tokens, error) {
	if l := len(refreshToken); l < 20 || l > 200 {
		return Tokens{}, errBadRefresh
	}
	newToken, newHash, err := auth.NewRefreshToken()
	if err != nil {
		return Tokens{}, err
	}
	res, err := s.repo.RotateSession(ctx, domain.RotateParams{
		OldHash:     auth.HashRefreshToken(refreshToken),
		NewHash:     newHash,
		TTL:         s.cfg.RefreshTTL,
		MaxLifetime: s.cfg.SessionMaxLifetime,
	})
	if err != nil {
		return Tokens{}, err
	}
	switch res.Outcome {
	case domain.RotateOK:
	case domain.RotateReuseDetected:
		s.log.Warn("refresh token reuse detected; session family revoked")
		return Tokens{}, errBadRefresh
	default:
		return Tokens{}, errBadRefresh
	}

	user, err := s.repo.UserByID(ctx, res.Session.UserID)
	if err != nil || user.Status != domain.StatusActive {
		if _, rerr := s.repo.RevokeFamily(ctx, res.Session.UserID, res.Session.FamilyID); rerr != nil {
			s.log.Warn("could not revoke session of inactive user")
		}
		return Tokens{}, errBadRefresh
	}
	access, err := s.tokens.Issue(user.ID, res.Session.FamilyID, res.Session.DeviceID)
	if err != nil {
		return Tokens{}, fmt.Errorf("issue access token: %w", err)
	}
	return Tokens{AccessToken: access, RefreshToken: newToken, ExpiresIn: s.tokens.TTL(), SessionID: res.Session.FamilyID, DeviceID: res.Session.DeviceID, User: user}, nil
}

// Logout revokes the caller's current session.
func (s *Service) Logout(ctx context.Context, userID, sessionID string) error {
	_, err := s.repo.RevokeFamily(ctx, userID, sessionID)
	return err
}

// ListSessions returns the caller's live sessions.
func (s *Service) ListSessions(ctx context.Context, userID string) ([]domain.SessionInfo, error) {
	return s.repo.ListActiveSessions(ctx, userID)
}

// RevokeSession revokes one of the caller's sessions.
func (s *Service) RevokeSession(ctx context.Context, userID, sessionID string) error {
	found, err := s.repo.RevokeFamily(ctx, userID, sessionID)
	if err != nil {
		return err
	}
	if !found {
		return domain.NotFound()
	}
	return nil
}

// ListDevices returns the caller's registered devices.
func (s *Service) ListDevices(ctx context.Context, userID string) ([]domain.Device, error) {
	return s.repo.ListDevices(ctx, userID)
}

// RevokeDevice revokes a device and its sessions.
func (s *Service) RevokeDevice(ctx context.Context, userID, deviceID string) error {
	return s.repo.RevokeDevice(ctx, userID, deviceID)
}

// Me returns the caller's profile.
func (s *Service) Me(ctx context.Context, userID string) (domain.User, error) {
	return s.repo.UserByID(ctx, userID)
}

// UpdateMe changes the caller's display name.
func (s *Service) UpdateMe(ctx context.Context, userID, displayName string) (domain.User, error) {
	name, err := CleanText(displayName, "Display name", 100)
	if err != nil {
		return domain.User{}, err
	}
	return s.repo.UpdateDisplayName(ctx, userID, name)
}

// LookupByEmail finds an active user by exact email, for starting a conversation. The result
// is deliberately minimal (id and display name). The endpoint is authenticated and rate-limited.
func (s *Service) LookupByEmail(ctx context.Context, email string) (domain.User, error) {
	e, err := NormalizeEmail(email)
	if err != nil {
		return domain.User{}, domain.NotFound()
	}
	u, _, err := s.repo.UserByEmail(ctx, e)
	if err != nil {
		return domain.User{}, err
	}
	if u.Status != domain.StatusActive {
		return domain.User{}, domain.NotFound()
	}
	return domain.User{ID: u.ID, DisplayName: u.DisplayName}, nil
}
