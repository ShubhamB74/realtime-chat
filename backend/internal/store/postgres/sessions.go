package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"realtime-chat/backend/internal/domain"
)

// CreateLoginSession registers (or reuses) a device and starts a new session family, atomically.
// The user row is locked so concurrent logins cannot exceed the device limit.
func (s *Store) CreateLoginSession(ctx context.Context, p domain.CreateLoginParams) (domain.Device, domain.Session, error) {
	var (
		dev  domain.Device
		sess domain.Session
	)
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var locked string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM users WHERE id = $1::uuid FOR UPDATE`, p.UserID).Scan(&locked); err != nil {
			return notFoundIfNoRows(err, "lock user")
		}

		if p.DeviceID != "" {
			err := tx.QueryRow(ctx,
				`UPDATE devices SET last_seen_at = now()
				 WHERE id = $1::uuid AND user_id = $2::uuid AND revoked_at IS NULL
				 RETURNING id::text, name, platform, last_seen_at, created_at`,
				p.DeviceID, p.UserID,
			).Scan(&dev.ID, &dev.Name, &dev.Platform, &dev.LastSeenAt, &dev.CreatedAt)
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.Invalid("Unknown device")
			}
			if err != nil {
				return fmt.Errorf("touch device: %w", err)
			}
		} else {
			var n int
			if err := tx.QueryRow(ctx,
				`SELECT count(*) FROM devices WHERE user_id = $1::uuid AND revoked_at IS NULL`, p.UserID).Scan(&n); err != nil {
				return fmt.Errorf("count devices: %w", err)
			}
			if p.MaxDevices > 0 && n >= p.MaxDevices {
				return domain.Conflict("Device limit reached; revoke an unused device first")
			}
			if err := tx.QueryRow(ctx,
				`INSERT INTO devices (user_id, name, platform, last_seen_at) VALUES ($1::uuid, $2, $3, now())
				 RETURNING id::text, name, platform, last_seen_at, created_at`,
				p.UserID, p.DeviceName, p.Platform,
			).Scan(&dev.ID, &dev.Name, &dev.Platform, &dev.LastSeenAt, &dev.CreatedAt); err != nil {
				return fmt.Errorf("insert device: %w", err)
			}
		}

		sess.UserID, sess.DeviceID = p.UserID, dev.ID
		if err := tx.QueryRow(ctx,
			`INSERT INTO user_sessions (user_id, device_id, family_id, refresh_token_hash, expires_at)
			 VALUES ($1::uuid, $2::uuid, uuidv7(), $3, now() + make_interval(secs => $4))
			 RETURNING id::text, family_id::text, expires_at`,
			p.UserID, dev.ID, p.TokenHash, p.TTL.Seconds(),
		).Scan(&sess.ID, &sess.FamilyID, &sess.ExpiresAt); err != nil {
			return fmt.Errorf("insert session: %w", err)
		}
		return nil
	})
	if err != nil {
		return domain.Device{}, domain.Session{}, err
	}
	return dev, sess, nil
}

// RotateSession exchanges a refresh token for a new one. Presenting a token that was already
// rotated is treated as theft: the whole family is revoked and RotateReuseDetected is returned.
// Revocation and rotation are committed even when the outcome is not RotateOK.
func (s *Store) RotateSession(ctx context.Context, p domain.RotateParams) (domain.RotateResult, error) {
	var res domain.RotateResult
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var (
			old           domain.Session
			rotatedAt     *time.Time
			revokedAt     *time.Time
			deviceRevoked *time.Time
			expired       bool
		)
		err := tx.QueryRow(ctx,
			`SELECT s.id::text, s.user_id::text, s.device_id::text, s.family_id::text,
			        s.rotated_at, s.revoked_at, d.revoked_at, s.expires_at <= now()
			 FROM user_sessions s JOIN devices d ON d.id = s.device_id
			 WHERE s.refresh_token_hash = $1
			 FOR UPDATE OF s`, p.OldHash,
		).Scan(&old.ID, &old.UserID, &old.DeviceID, &old.FamilyID, &rotatedAt, &revokedAt, &deviceRevoked, &expired)
		if errors.Is(err, pgx.ErrNoRows) {
			res.Outcome = domain.RotateNotFound
			return nil
		}
		if err != nil {
			return fmt.Errorf("load session: %w", err)
		}

		switch {
		case revokedAt != nil || deviceRevoked != nil:
			res.Outcome = domain.RotateRevoked
			return nil
		case rotatedAt != nil:
			if _, err := tx.Exec(ctx,
				`UPDATE user_sessions SET revoked_at = now() WHERE family_id = $1::uuid AND revoked_at IS NULL`, old.FamilyID); err != nil {
				return fmt.Errorf("revoke family: %w", err)
			}
			res.Outcome = domain.RotateReuseDetected
			return nil
		case expired:
			res.Outcome = domain.RotateExpired
			return nil
		}

		// New expiry: sliding TTL, capped by the family's absolute maximum lifetime.
		var newExpiry time.Time
		var stillValid bool
		if err := tx.QueryRow(ctx,
			`SELECT exp, exp > now() FROM (
			   SELECT least(now() + make_interval(secs => $2), min(created_at) + make_interval(secs => $3)) AS exp
			   FROM user_sessions WHERE family_id = $1::uuid) t`,
			old.FamilyID, p.TTL.Seconds(), p.MaxLifetime.Seconds(),
		).Scan(&newExpiry, &stillValid); err != nil {
			return fmt.Errorf("compute expiry: %w", err)
		}
		if !stillValid {
			res.Outcome = domain.RotateExpired
			return nil
		}

		if _, err := tx.Exec(ctx, `UPDATE user_sessions SET rotated_at = now() WHERE id = $1::uuid`, old.ID); err != nil {
			return fmt.Errorf("mark rotated: %w", err)
		}
		res.Session = domain.Session{UserID: old.UserID, DeviceID: old.DeviceID, FamilyID: old.FamilyID}
		if err := tx.QueryRow(ctx,
			`INSERT INTO user_sessions (user_id, device_id, family_id, refresh_token_hash, expires_at)
			 VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5) RETURNING id::text, expires_at`,
			old.UserID, old.DeviceID, old.FamilyID, p.NewHash, newExpiry,
		).Scan(&res.Session.ID, &res.Session.ExpiresAt); err != nil {
			return fmt.Errorf("insert rotated session: %w", err)
		}
		res.Outcome = domain.RotateOK
		return nil
	})
	if err != nil {
		return domain.RotateResult{}, err
	}
	return res, nil
}

// RevokeFamily revokes every live token of a session family owned by userID.
// It reports whether the session exists for that user (revoking twice is not an error).
func (s *Store) RevokeFamily(ctx context.Context, userID, familyID string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE user_sessions SET revoked_at = now()
		 WHERE family_id = $1::uuid AND user_id = $2::uuid AND revoked_at IS NULL`, familyID, userID)
	if err != nil {
		return false, fmt.Errorf("revoke family: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	var one int
	err = s.pool.QueryRow(ctx,
		`SELECT 1 FROM user_sessions WHERE family_id = $1::uuid AND user_id = $2::uuid LIMIT 1`, familyID, userID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check session: %w", err)
	}
	return true, nil
}

// ListActiveSessions returns one entry per live session family of the user.
func (s *Store) ListActiveSessions(ctx context.Context, userID string) ([]domain.SessionInfo, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT s.family_id::text, s.device_id::text, d.name, d.platform,
		        (SELECT min(f.created_at) FROM user_sessions f WHERE f.family_id = s.family_id),
		        s.created_at, s.expires_at
		 FROM user_sessions s JOIN devices d ON d.id = s.device_id
		 WHERE s.user_id = $1::uuid AND s.rotated_at IS NULL AND s.revoked_at IS NULL AND s.expires_at > now()
		 ORDER BY s.created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()
	var out []domain.SessionInfo
	for rows.Next() {
		var si domain.SessionInfo
		if err := rows.Scan(&si.ID, &si.DeviceID, &si.DeviceName, &si.Platform, &si.StartedAt, &si.LastRefreshedAt, &si.ExpiresAt); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		out = append(out, si)
	}
	return out, rows.Err()
}

// ListDevices returns the user's non-revoked devices, newest first.
func (s *Store) ListDevices(ctx context.Context, userID string) ([]domain.Device, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id::text, name, platform, last_seen_at, created_at FROM devices
		 WHERE user_id = $1::uuid AND revoked_at IS NULL ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	defer rows.Close()
	var out []domain.Device
	for rows.Next() {
		var d domain.Device
		if err := rows.Scan(&d.ID, &d.Name, &d.Platform, &d.LastSeenAt, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan device: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RevokeDevice revokes a device and all of its sessions. Returns NotFound if the device does
// not exist, belongs to another user, or is already revoked.
func (s *Store) RevokeDevice(ctx context.Context, userID, deviceID string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx,
			`UPDATE devices SET revoked_at = now()
			 WHERE id = $1::uuid AND user_id = $2::uuid AND revoked_at IS NULL RETURNING id::text`, deviceID, userID).Scan(&id)
		if err != nil {
			return notFoundIfNoRows(err, "revoke device")
		}
		if _, err := tx.Exec(ctx,
			`UPDATE user_sessions SET revoked_at = now()
			 WHERE device_id = $1::uuid AND user_id = $2::uuid AND revoked_at IS NULL`, deviceID, userID); err != nil {
			return fmt.Errorf("revoke device sessions: %w", err)
		}
		return nil
	})
}
