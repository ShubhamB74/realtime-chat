package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"realtime-chat/backend/internal/domain"
)

const msgCols = `id::text, conversation_id::text, sender_id::text, seq, mod_seq, body, edited_at, deleted_at, created_at`

func scanMessage(row pgx.Row) (domain.Message, error) {
	var m domain.Message
	err := row.Scan(&m.ID, &m.ConversationID, &m.SenderID, &m.Seq, &m.ModSeq, &m.Body, &m.EditedAt, &m.DeletedAt, &m.CreatedAt)
	return m, err
}

// lockMember locks the conversation row and verifies that userID is an active member, in one
// statement. The lock serializes message writes, membership changes and counter bumps for the
// conversation, so a member removed concurrently cannot slip a write in. NotFound if the
// conversation does not exist or the user is not an active member.
func lockMember(ctx context.Context, tx pgx.Tx, convID, userID string) (role string, joinedSeq int64, err error) {
	err = tx.QueryRow(ctx,
		`SELECT m.role, m.joined_seq
		 FROM conversations c
		 JOIN conversation_members m ON m.conversation_id = c.id AND m.user_id = $2::uuid AND m.removed_at IS NULL
		 WHERE c.id = $1::uuid
		 FOR UPDATE OF c`, convID, userID).Scan(&role, &joinedSeq)
	if err != nil {
		return "", 0, notFoundIfNoRows(err, "lock conversation member")
	}
	return role, joinedSeq, nil
}

// bumpCounter increments the conversation's counter and returns the new value. The caller must
// hold the row lock (lockMember). Because it runs inside the message transaction, a rollback
// undoes the increment, so sequence numbers are gapless and become visible in commit order.
func bumpCounter(ctx context.Context, tx pgx.Tx, convID string) (int64, error) {
	var n int64
	err := tx.QueryRow(ctx, `UPDATE conversations SET last_seq = last_seq + 1 WHERE id = $1::uuid RETURNING last_seq`, convID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("bump counter: %w", err)
	}
	return n, nil
}

func activeMemberIDs(ctx context.Context, tx pgx.Tx, convID string) ([]string, error) {
	rows, err := tx.Query(ctx,
		`SELECT user_id::text FROM conversation_members WHERE conversation_id = $1::uuid AND removed_at IS NULL`, convID)
	if err != nil {
		return nil, fmt.Errorf("list recipients: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan recipient: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SendMessage persists a message and returns it with the recipient list. The message is
// committed before this returns, which is what lets callers acknowledge acceptance (ADR-003).
// A retry with the same (device, idempotency key) returns the original message unchanged.
// (The transactional outbox row is added to this transaction in Phase 4B.)
func (s *Store) SendMessage(ctx context.Context, p domain.SendParams) (domain.MessageResult, error) {
	var res domain.MessageResult
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if _, _, err := lockMember(ctx, tx, p.ConversationID, p.UserID); err != nil {
			return err
		}

		existing, err := scanMessage(tx.QueryRow(ctx,
			`SELECT `+msgCols+` FROM messages WHERE sender_device_id = $1::uuid AND idempotency_key = $2`,
			p.DeviceID, p.IdempotencyKey))
		switch {
		case err == nil:
			if existing.ConversationID != p.ConversationID {
				return domain.Conflict("This idempotency key was already used for a different request")
			}
			existing.IdempotencyKey = p.IdempotencyKey
			res = domain.MessageResult{Message: existing, Duplicate: true}
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("check idempotency key: %w", err)
		}

		seq, err := bumpCounter(ctx, tx, p.ConversationID)
		if err != nil {
			return err
		}
		msg, err := scanMessage(tx.QueryRow(ctx,
			`INSERT INTO messages (conversation_id, sender_id, sender_device_id, idempotency_key, seq, mod_seq, body)
			 VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $5, $6) RETURNING `+msgCols,
			p.ConversationID, p.UserID, p.DeviceID, p.IdempotencyKey, seq, p.Body))
		if err != nil {
			return fmt.Errorf("insert message: %w", err)
		}
		msg.IdempotencyKey = p.IdempotencyKey
		recipients, err := activeMemberIDs(ctx, tx, p.ConversationID)
		if err != nil {
			return err
		}
		res = domain.MessageResult{Message: msg, Recipients: recipients}
		return nil
	})
	if err != nil {
		return domain.MessageResult{}, err
	}
	return res, nil
}

// loadVisibleMessage loads sender/state for a message the member is allowed to see.
func loadVisibleMessage(ctx context.Context, tx pgx.Tx, convID, msgID string, joinedSeq int64) (sender *string, deleted bool, err error) {
	var (
		seq       int64
		deletedAt *string
	)
	err = tx.QueryRow(ctx,
		`SELECT sender_id::text, seq, deleted_at::text FROM messages WHERE id = $1::uuid AND conversation_id = $2::uuid`,
		msgID, convID).Scan(&sender, &seq, &deletedAt)
	if err != nil {
		return nil, false, notFoundIfNoRows(err, "load message")
	}
	if seq <= joinedSeq {
		return nil, false, domain.NotFound() // from before the caller joined: invisible
	}
	return sender, deletedAt != nil, nil
}

// EditMessage replaces the body of the caller's own message and bumps its mod_seq.
func (s *Store) EditMessage(ctx context.Context, convID, msgID, userID, body string) (domain.MessageResult, error) {
	var res domain.MessageResult
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		_, joinedSeq, err := lockMember(ctx, tx, convID, userID)
		if err != nil {
			return err
		}
		sender, deleted, err := loadVisibleMessage(ctx, tx, convID, msgID, joinedSeq)
		if err != nil {
			return err
		}
		if sender == nil || *sender != userID {
			return domain.Forbidden("You can only edit your own messages")
		}
		if deleted {
			return domain.Conflict("The message was deleted")
		}
		modSeq, err := bumpCounter(ctx, tx, convID)
		if err != nil {
			return err
		}
		msg, err := scanMessage(tx.QueryRow(ctx,
			`UPDATE messages SET body = $3, mod_seq = $4, edited_at = now()
			 WHERE id = $1::uuid AND conversation_id = $2::uuid RETURNING `+msgCols, msgID, convID, body, modSeq))
		if err != nil {
			return fmt.Errorf("update message: %w", err)
		}
		recipients, err := activeMemberIDs(ctx, tx, convID)
		if err != nil {
			return err
		}
		res = domain.MessageResult{Message: msg, Recipients: recipients}
		return nil
	})
	if err != nil {
		return domain.MessageResult{}, err
	}
	return res, nil
}

// DeleteMessage turns a message into a tombstone. The sender may delete their own message;
// a group admin may delete anyone's. Deleting an already-deleted message is a no-op.
func (s *Store) DeleteMessage(ctx context.Context, convID, msgID, userID string) (domain.MessageResult, error) {
	var res domain.MessageResult
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		role, joinedSeq, err := lockMember(ctx, tx, convID, userID)
		if err != nil {
			return err
		}
		sender, deleted, err := loadVisibleMessage(ctx, tx, convID, msgID, joinedSeq)
		if err != nil {
			return err
		}
		own := sender != nil && *sender == userID
		if !own && role != domain.RoleAdmin {
			return domain.Forbidden("You can only delete your own messages")
		}
		if deleted {
			msg, err := scanMessage(tx.QueryRow(ctx, `SELECT `+msgCols+` FROM messages WHERE id = $1::uuid`, msgID))
			if err != nil {
				return fmt.Errorf("load message: %w", err)
			}
			res = domain.MessageResult{Message: msg, Duplicate: true}
			return nil
		}
		modSeq, err := bumpCounter(ctx, tx, convID)
		if err != nil {
			return err
		}
		msg, err := scanMessage(tx.QueryRow(ctx,
			`UPDATE messages SET body = NULL, deleted_at = now(), mod_seq = $3
			 WHERE id = $1::uuid AND conversation_id = $2::uuid RETURNING `+msgCols, msgID, convID, modSeq))
		if err != nil {
			return fmt.Errorf("tombstone message: %w", err)
		}
		recipients, err := activeMemberIDs(ctx, tx, convID)
		if err != nil {
			return err
		}
		res = domain.MessageResult{Message: msg, Recipients: recipients}
		return nil
	})
	if err != nil {
		return domain.MessageResult{}, err
	}
	return res, nil
}

// joinedSeqOf returns the caller's join point, or NotFound if they are not an active member.
func (s *Store) joinedSeqOf(ctx context.Context, convID, userID string) (int64, error) {
	var j int64
	err := s.pool.QueryRow(ctx,
		`SELECT joined_seq FROM conversation_members
		 WHERE conversation_id = $1::uuid AND user_id = $2::uuid AND removed_at IS NULL`, convID, userID).Scan(&j)
	if err != nil {
		return 0, notFoundIfNoRows(err, "membership")
	}
	return j, nil
}

func collectMessages(rows pgx.Rows) ([]domain.Message, error) {
	defer rows.Close()
	var out []domain.Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListHistory returns messages newest first, strictly older than beforeSeq (0 means latest),
// limited to what the member may see (seq greater than their join point).
func (s *Store) ListHistory(ctx context.Context, convID, userID string, beforeSeq int64, limit int) ([]domain.Message, bool, error) {
	joined, err := s.joinedSeqOf(ctx, convID, userID)
	if err != nil {
		return nil, false, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+msgCols+` FROM messages
		 WHERE conversation_id = $1::uuid AND seq > $2 AND ($3::bigint = 0 OR seq < $3::bigint)
		 ORDER BY seq DESC LIMIT $4`, convID, joined, beforeSeq, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list history: %w", err)
	}
	msgs, err := collectMessages(rows)
	if err != nil {
		return nil, false, err
	}
	more := len(msgs) > limit
	if more {
		msgs = msgs[:limit]
	}
	return msgs, more, nil
}

// ListChanges returns messages whose mod_seq is greater than afterModSeq, oldest change first:
// new messages, edits and deletions alike. This is the sync primitive (ADR-005).
func (s *Store) ListChanges(ctx context.Context, convID, userID string, afterModSeq int64, limit int) (domain.Changes, error) {
	joined, err := s.joinedSeqOf(ctx, convID, userID)
	if err != nil {
		return domain.Changes{}, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+msgCols+` FROM messages
		 WHERE conversation_id = $1::uuid AND mod_seq > $2 AND seq > $3
		 ORDER BY mod_seq ASC LIMIT $4`, convID, afterModSeq, joined, limit+1)
	if err != nil {
		return domain.Changes{}, fmt.Errorf("list changes: %w", err)
	}
	msgs, err := collectMessages(rows)
	if err != nil {
		return domain.Changes{}, err
	}
	var ch domain.Changes
	if len(msgs) > limit {
		ch.HasMore = true
		msgs = msgs[:limit]
	}
	ch.Messages = msgs
	if err := s.pool.QueryRow(ctx, `SELECT last_seq FROM conversations WHERE id = $1::uuid`, convID).Scan(&ch.LastSeq); err != nil {
		return domain.Changes{}, notFoundIfNoRows(err, "conversation counter")
	}
	return ch, nil
}

// MemberIDs authorizes the caller and returns all active member ids in one query. Not a member
// (or no such conversation) yields NotFound.
func (s *Store) MemberIDs(ctx context.Context, convID, userID string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT m2.user_id::text
		 FROM conversation_members m1
		 JOIN conversation_members m2 ON m2.conversation_id = m1.conversation_id AND m2.removed_at IS NULL
		 WHERE m1.conversation_id = $1::uuid AND m1.user_id = $2::uuid AND m1.removed_at IS NULL`, convID, userID)
	if err != nil {
		return nil, fmt.Errorf("member ids: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan member id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("member ids: %w", err)
	}
	if len(ids) == 0 {
		return nil, domain.NotFound()
	}
	return ids, nil
}

// SessionActive reports whether the session family is still live for this user and device:
// its current token is unrotated, unrevoked and unexpired, the device is not revoked and the
// user is active. Used at WebSocket handshake and periodically while connected (ADR-009).
func (s *Store) SessionActive(ctx context.Context, userID, familyID, deviceID string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (
		   SELECT 1 FROM user_sessions s
		   JOIN devices d ON d.id = s.device_id
		   JOIN users u ON u.id = s.user_id
		   WHERE s.family_id = $1::uuid AND s.user_id = $2::uuid AND s.device_id = $3::uuid
		     AND s.rotated_at IS NULL AND s.revoked_at IS NULL AND s.expires_at > now()
		     AND d.revoked_at IS NULL AND u.status = 'active')`, familyID, userID, deviceID).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("session active: %w", err)
	}
	return ok, nil
}
