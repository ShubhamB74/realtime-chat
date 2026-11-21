package postgres

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"realtime-chat/backend/internal/domain"
)

// ApplyCursors applies a batch of delivery acknowledgements and read marks and returns the
// resulting receipt state of every (conversation, user) pair that was actually updated.
//
// Everything is validated in SQL, so a client cannot move cursors in conversations it does not
// belong to, with someone else's device, or past the conversation counter. Items that fail
// validation are silently skipped. Cursors only move forward (GREATEST).
func (s *Store) ApplyCursors(ctx context.Context, acks []domain.DeviceAck, reads []domain.ReadMark) ([]domain.ReceiptState, error) {
	if len(acks) == 0 && len(reads) == 0 {
		return nil, nil
	}
	// A stable order avoids lock-order deadlocks between concurrent flushes.
	sort.Slice(acks, func(i, j int) bool {
		if acks[i].DeviceID != acks[j].DeviceID {
			return acks[i].DeviceID < acks[j].DeviceID
		}
		return acks[i].ConversationID < acks[j].ConversationID
	})
	sort.Slice(reads, func(i, j int) bool {
		if reads[i].UserID != reads[j].UserID {
			return reads[i].UserID < reads[j].UserID
		}
		return reads[i].ConversationID < reads[j].ConversationID
	})

	var states []domain.ReceiptState
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		type pair struct{ conv, user string }
		seen := map[pair]bool{}
		var order []pair
		touch := func(conv, user string) {
			p := pair{conv, user}
			if !seen[p] {
				seen[p] = true
				order = append(order, p)
			}
		}

		for _, a := range acks {
			tag, err := tx.Exec(ctx,
				`INSERT INTO device_cursors (device_id, conversation_id, acked_mod_seq)
				 SELECT $1::uuid, c.id, LEAST($3::bigint, c.last_seq)
				 FROM conversations c
				 JOIN conversation_members m ON m.conversation_id = c.id AND m.user_id = $4::uuid AND m.removed_at IS NULL
				 WHERE c.id = $2::uuid
				   AND EXISTS (SELECT 1 FROM devices d WHERE d.id = $1::uuid AND d.user_id = $4::uuid AND d.revoked_at IS NULL)
				 ON CONFLICT (device_id, conversation_id) DO UPDATE
				   SET acked_mod_seq = GREATEST(device_cursors.acked_mod_seq, EXCLUDED.acked_mod_seq), updated_at = now()`,
				a.DeviceID, a.ConversationID, a.UpToModSeq, a.UserID)
			if err != nil {
				return fmt.Errorf("apply ack: %w", err)
			}
			if tag.RowsAffected() > 0 {
				touch(a.ConversationID, a.UserID)
			}
		}
		for _, r := range reads {
			tag, err := tx.Exec(ctx,
				`UPDATE conversation_members m
				 SET read_seq = GREATEST(m.read_seq, LEAST($3::bigint, c.last_seq))
				 FROM conversations c
				 WHERE c.id = m.conversation_id AND m.conversation_id = $1::uuid
				   AND m.user_id = $2::uuid AND m.removed_at IS NULL`,
				r.ConversationID, r.UserID, r.UpToSeq)
			if err != nil {
				return fmt.Errorf("apply read mark: %w", err)
			}
			if tag.RowsAffected() > 0 {
				touch(r.ConversationID, r.UserID)
			}
		}

		for _, p := range order {
			st := domain.ReceiptState{ConversationID: p.conv, UserID: p.user}
			var typ string
			err := tx.QueryRow(ctx,
				`SELECT c.type, m.read_seq,
				        COALESCE((SELECT max(dc.acked_mod_seq) FROM device_cursors dc JOIN devices d ON d.id = dc.device_id
				                  WHERE dc.conversation_id = c.id AND d.user_id = m.user_id AND d.revoked_at IS NULL), 0)
				 FROM conversations c
				 JOIN conversation_members m ON m.conversation_id = c.id AND m.user_id = $2::uuid AND m.removed_at IS NULL
				 WHERE c.id = $1::uuid`, p.conv, p.user).Scan(&typ, &st.Read, &st.Delivered)
			if err != nil {
				return fmt.Errorf("load receipt state: %w", err)
			}
			if st.Direct = typ == domain.ConvDirect; st.Direct {
				rows, err := tx.Query(ctx,
					`SELECT user_id::text FROM conversation_members
					 WHERE conversation_id = $1::uuid AND user_id <> $2::uuid AND removed_at IS NULL`, p.conv, p.user)
				if err != nil {
					return fmt.Errorf("load other members: %w", err)
				}
				for rows.Next() {
					var id string
					if err := rows.Scan(&id); err != nil {
						rows.Close()
						return fmt.Errorf("scan member: %w", err)
					}
					st.Others = append(st.Others, id)
				}
				rows.Close()
				if err := rows.Err(); err != nil {
					return fmt.Errorf("load other members: %w", err)
				}
			}
			states = append(states, st)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return states, nil
}

// ListReceipts returns every active member's delivered and read position (the "seen by" view).
// The caller must be an active member.
func (s *Store) ListReceipts(ctx context.Context, convID, userID string) ([]domain.MemberReceipt, error) {
	if _, err := s.joinedSeqOf(ctx, convID, userID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT m.user_id::text, m.read_seq,
		        COALESCE((SELECT max(dc.acked_mod_seq) FROM device_cursors dc JOIN devices d ON d.id = dc.device_id
		                  WHERE dc.conversation_id = m.conversation_id AND d.user_id = m.user_id AND d.revoked_at IS NULL), 0)
		 FROM conversation_members m
		 WHERE m.conversation_id = $1::uuid AND m.removed_at IS NULL
		 ORDER BY m.joined_at, m.user_id`, convID)
	if err != nil {
		return nil, fmt.Errorf("list receipts: %w", err)
	}
	defer rows.Close()
	var out []domain.MemberReceipt
	for rows.Next() {
		var r domain.MemberReceipt
		if err := rows.Scan(&r.UserID, &r.Read, &r.Delivered); err != nil {
			return nil, fmt.Errorf("scan receipt: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PendingConversations finds the conversations that have changes beyond this device's cursor
// with one indexed query (ADR-005), most recently active first. The cursor of a device that has
// never acknowledged anything in a conversation starts at the member's join point.
func (s *Store) PendingConversations(ctx context.Context, userID, deviceID string, limit int) ([]domain.PendingSync, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT c.id::text, GREATEST(COALESCE(dc.acked_mod_seq, 0), m.joined_seq)
		 FROM conversation_members m
		 JOIN conversations c ON c.id = m.conversation_id
		 LEFT JOIN device_cursors dc ON dc.conversation_id = c.id AND dc.device_id = $2::uuid
		 WHERE m.user_id = $1::uuid AND m.removed_at IS NULL
		   AND c.last_seq > GREATEST(COALESCE(dc.acked_mod_seq, 0), m.joined_seq)
		 ORDER BY c.updated_at DESC, c.id DESC
		 LIMIT $3`, userID, deviceID, limit)
	if err != nil {
		return nil, fmt.Errorf("pending conversations: %w", err)
	}
	defer rows.Close()
	var out []domain.PendingSync
	for rows.Next() {
		var p domain.PendingSync
		if err := rows.Scan(&p.ConversationID, &p.Cursor); err != nil {
			return nil, fmt.Errorf("scan pending: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// VisibleUsers returns which of the candidates share an active conversation with the viewer
// whose size is at most maxGroup. It authorizes presence subscriptions: you may watch someone's
// presence only if you already share a conversation with them, and never in large groups
// (ADR-007).
func (s *Store) VisibleUsers(ctx context.Context, viewerID string, candidates []string, maxGroup int) ([]string, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT m2.user_id::text
		 FROM conversation_members m1
		 JOIN conversation_members m2 ON m2.conversation_id = m1.conversation_id AND m2.removed_at IS NULL
		      AND m2.user_id = ANY($2::text[]::uuid[]) AND m2.user_id <> m1.user_id
		 WHERE m1.user_id = $1::uuid AND m1.removed_at IS NULL
		   AND (SELECT count(*) FROM conversation_members x
		        WHERE x.conversation_id = m1.conversation_id AND x.removed_at IS NULL) <= $3`,
		viewerID, candidates, maxGroup)
	if err != nil {
		return nil, fmt.Errorf("visible users: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
