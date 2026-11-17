package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"realtime-chat/backend/internal/domain"
)

const convCols = `id::text, type, title, created_by::text, last_seq, created_at, updated_at`

func scanConversation(row pgx.Row) (domain.Conversation, error) {
	var c domain.Conversation
	err := row.Scan(&c.ID, &c.Type, &c.Title, &c.CreatedBy, &c.LastSeq, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

func directKey(a, b string) string {
	ids := []string{a, b}
	sort.Strings(ids)
	return ids[0] + ":" + ids[1]
}

// GetOrCreateDirect returns the single direct conversation between two users, creating it
// (with both members and default settings) if needed. Safe under concurrent calls: the unique
// direct_key makes one INSERT win and the other fall back to the existing row.
func (s *Store) GetOrCreateDirect(ctx context.Context, userA, userB string) (domain.Conversation, bool, error) {
	var (
		conv    domain.Conversation
		created bool
	)
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		key := directKey(userA, userB)
		var id string
		err := tx.QueryRow(ctx,
			`INSERT INTO conversations (type, direct_key, created_by) VALUES ('direct', $1, $2::uuid)
			 ON CONFLICT (direct_key) DO NOTHING RETURNING id::text`, key, userA).Scan(&id)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if err := tx.QueryRow(ctx, `SELECT id::text FROM conversations WHERE direct_key = $1`, key).Scan(&id); err != nil {
				return fmt.Errorf("load existing direct conversation: %w", err)
			}
		case err != nil:
			return fmt.Errorf("insert direct conversation: %w", err)
		default:
			created = true
			if _, err := tx.Exec(ctx, `INSERT INTO conversation_settings (conversation_id) VALUES ($1::uuid)`, id); err != nil {
				return fmt.Errorf("insert settings: %w", err)
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO conversation_members (conversation_id, user_id, role)
				 VALUES ($1::uuid, $2::uuid, 'member'), ($1::uuid, $3::uuid, 'member')`, id, userA, userB); err != nil {
				return fmt.Errorf("insert members: %w", err)
			}
		}
		c, err := scanConversation(tx.QueryRow(ctx, `SELECT `+convCols+` FROM conversations WHERE id = $1::uuid`, id))
		if err != nil {
			return fmt.Errorf("load conversation: %w", err)
		}
		conv = c
		return nil
	})
	if err != nil {
		return domain.Conversation{}, false, err
	}
	return conv, created, nil
}

// CreateGroup creates a group with the creator as admin and the others as members.
func (s *Store) CreateGroup(ctx context.Context, creator, title string, memberIDs []string) (domain.Conversation, error) {
	var conv domain.Conversation
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx,
			`INSERT INTO conversations (type, title, created_by) VALUES ('group', $1, $2::uuid) RETURNING id::text`,
			title, creator).Scan(&id); err != nil {
			return fmt.Errorf("insert group: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO conversation_settings (conversation_id) VALUES ($1::uuid)`, id); err != nil {
			return fmt.Errorf("insert settings: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO conversation_members (conversation_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'admin')`,
			id, creator); err != nil {
			return fmt.Errorf("insert creator: %w", err)
		}
		if len(memberIDs) > 0 {
			if _, err := tx.Exec(ctx,
				`INSERT INTO conversation_members (conversation_id, user_id, role)
				 SELECT $1::uuid, u, 'member' FROM unnest($2::text[]::uuid[]) AS u`, id, memberIDs); err != nil {
				return fmt.Errorf("insert members: %w", err)
			}
		}
		c, err := scanConversation(tx.QueryRow(ctx, `SELECT `+convCols+` FROM conversations WHERE id = $1::uuid`, id))
		if err != nil {
			return fmt.Errorf("load conversation: %w", err)
		}
		conv = c
		return nil
	})
	if err != nil {
		return domain.Conversation{}, err
	}
	return conv, nil
}

// Membership returns the user's active membership, or NotFound. This is the authorization
// primitive: a removed member and a stranger are indistinguishable.
func (s *Store) Membership(ctx context.Context, convID, userID string) (domain.Member, error) {
	var m domain.Member
	err := s.pool.QueryRow(ctx,
		`SELECT m.user_id::text, u.display_name, m.role, m.read_seq, m.joined_at
		 FROM conversation_members m JOIN users u ON u.id = m.user_id
		 WHERE m.conversation_id = $1::uuid AND m.user_id = $2::uuid AND m.removed_at IS NULL`,
		convID, userID).Scan(&m.UserID, &m.DisplayName, &m.Role, &m.ReadSeq, &m.JoinedAt)
	if err != nil {
		return domain.Member{}, notFoundIfNoRows(err, "membership")
	}
	return m, nil
}

// GetConversation loads a conversation by id (callers must authorize first).
func (s *Store) GetConversation(ctx context.Context, convID string) (domain.Conversation, error) {
	c, err := scanConversation(s.pool.QueryRow(ctx, `SELECT `+convCols+` FROM conversations WHERE id = $1::uuid`, convID))
	if err != nil {
		return domain.Conversation{}, notFoundIfNoRows(err, "get conversation")
	}
	return c, nil
}

// ActiveMemberCount returns the number of active members.
func (s *Store) ActiveMemberCount(ctx context.Context, convID string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM conversation_members WHERE conversation_id = $1::uuid AND removed_at IS NULL`, convID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count members: %w", err)
	}
	return n, nil
}

// ListForUser returns the user's conversations, most recently updated first, using keyset
// pagination on (updated_at, id). It returns up to limit rows and the cursor for the next page.
func (s *Store) ListForUser(ctx context.Context, userID string, after *domain.ListCursor, limit int) ([]domain.ConversationSummary, *domain.ListCursor, error) {
	var (
		afterTime any
		afterID   any
	)
	if after != nil {
		afterTime, afterID = after.UpdatedAt, after.ID
	}
	rows, err := s.pool.Query(ctx,
		`SELECT c.id::text, c.type, c.title, c.created_by::text, c.last_seq, c.created_at, c.updated_at,
		        m.role,
		        (SELECT count(*) FROM conversation_members x WHERE x.conversation_id = c.id AND x.removed_at IS NULL),
		        p.peer_id, p.peer_name,
		        l.id, l.seq, l.sender_id, l.body, l.deleted, l.created_at
		 FROM conversation_members m
		 JOIN conversations c ON c.id = m.conversation_id
		 LEFT JOIN LATERAL (
		     SELECT u.id::text AS peer_id, u.display_name AS peer_name
		     FROM conversation_members pm JOIN users u ON u.id = pm.user_id
		     WHERE c.type = 'direct' AND pm.conversation_id = c.id AND pm.user_id <> m.user_id
		     LIMIT 1) p ON true
		 LEFT JOIN LATERAL (
		     SELECT lm.id::text AS id, lm.seq AS seq, lm.sender_id::text AS sender_id, lm.body AS body,
		            (lm.deleted_at IS NOT NULL) AS deleted, lm.created_at AS created_at
		     FROM messages lm
		     WHERE lm.conversation_id = c.id AND lm.seq > m.joined_seq
		     ORDER BY lm.seq DESC LIMIT 1) l ON true
		 WHERE m.user_id = $1::uuid AND m.removed_at IS NULL
		   AND ($2::timestamptz IS NULL OR (c.updated_at, c.id) < ($2::timestamptz, $3::text::uuid))
		 ORDER BY c.updated_at DESC, c.id DESC
		 LIMIT $4`, userID, afterTime, afterID, limit+1)
	if err != nil {
		return nil, nil, fmt.Errorf("list conversations: %w", err)
	}
	defer rows.Close()

	var out []domain.ConversationSummary
	for rows.Next() {
		var (
			cs       domain.ConversationSummary
			peerID   *string
			peerName *string
			lmID     *string
			lmSeq    *int64
			lmSender *string
			lmBody   *string
			lmDel    *bool
			lmAt     *time.Time
		)
		if err := rows.Scan(&cs.ID, &cs.Type, &cs.Title, &cs.CreatedBy, &cs.LastSeq, &cs.CreatedAt, &cs.UpdatedAt,
			&cs.Role, &cs.MemberCount, &peerID, &peerName,
			&lmID, &lmSeq, &lmSender, &lmBody, &lmDel, &lmAt); err != nil {
			return nil, nil, fmt.Errorf("scan conversation: %w", err)
		}
		if peerID != nil && peerName != nil {
			cs.Peer = &domain.Peer{UserID: *peerID, DisplayName: *peerName}
		}
		if lmID != nil && lmSeq != nil && lmDel != nil && lmAt != nil {
			cs.LastMessage = &domain.MessagePreview{ID: *lmID, Seq: *lmSeq, SenderID: lmSender, Deleted: *lmDel, CreatedAt: *lmAt}
			if lmBody != nil {
				p := truncateRunes(*lmBody, 140)
				cs.LastMessage.Body = &p
			}
		}
		out = append(out, cs)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("list conversations: %w", err)
	}

	var next *domain.ListCursor
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = &domain.ListCursor{UpdatedAt: last.UpdatedAt, ID: last.ID}
	}
	return out, next, nil
}

// UpdateTitle renames a group.
func (s *Store) UpdateTitle(ctx context.Context, convID, title string) (domain.Conversation, error) {
	c, err := scanConversation(s.pool.QueryRow(ctx,
		`UPDATE conversations SET title = $2 WHERE id = $1::uuid AND type = 'group' RETURNING `+convCols, convID, title))
	if err != nil {
		return domain.Conversation{}, notFoundIfNoRows(err, "update title")
	}
	return c, nil
}

// ListMembers returns active members ordered by join time.
func (s *Store) ListMembers(ctx context.Context, convID string) ([]domain.Member, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT m.user_id::text, u.display_name, m.role, m.read_seq, m.joined_at
		 FROM conversation_members m JOIN users u ON u.id = m.user_id
		 WHERE m.conversation_id = $1::uuid AND m.removed_at IS NULL
		 ORDER BY m.joined_at, m.user_id`, convID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()
	var out []domain.Member
	for rows.Next() {
		var m domain.Member
		if err := rows.Scan(&m.UserID, &m.DisplayName, &m.Role, &m.ReadSeq, &m.JoinedAt); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// lockConversation locks the conversation row to serialize membership changes and returns its type.
func lockConversation(ctx context.Context, tx pgx.Tx, convID string) (string, error) {
	var typ string
	err := tx.QueryRow(ctx, `SELECT type FROM conversations WHERE id = $1::uuid FOR UPDATE`, convID).Scan(&typ)
	if err != nil {
		return "", notFoundIfNoRows(err, "lock conversation")
	}
	return typ, nil
}

func countAdmins(ctx context.Context, tx pgx.Tx, convID string) (int, error) {
	var n int
	err := tx.QueryRow(ctx,
		`SELECT count(*) FROM conversation_members
		 WHERE conversation_id = $1::uuid AND removed_at IS NULL AND role = 'admin'`, convID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count admins: %w", err)
	}
	return n, nil
}

// AddMembers adds users to a group and returns the ids that were actually added (users who
// are already active members are skipped). Fails with a conflict if the group would exceed
// maxMembers; nothing is changed in that case.
func (s *Store) AddMembers(ctx context.Context, convID string, userIDs []string, maxMembers int) ([]string, error) {
	var added []string
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		typ, err := lockConversation(ctx, tx, convID)
		if err != nil {
			return err
		}
		if typ != domain.ConvGroup {
			return domain.Invalid("Members can only be added to group conversations")
		}
		rows, err := tx.Query(ctx,
			`INSERT INTO conversation_members (conversation_id, user_id, role, joined_seq)
			 SELECT $1::uuid, u, 'member', (SELECT last_seq FROM conversations WHERE id = $1::uuid)
			 FROM unnest($2::text[]::uuid[]) AS u
			 ON CONFLICT (conversation_id, user_id) DO UPDATE
			   SET removed_at = NULL, joined_at = now(), role = 'member', read_seq = 0,
			       joined_seq = EXCLUDED.joined_seq
			   WHERE conversation_members.removed_at IS NOT NULL
			 RETURNING user_id::text`, convID, userIDs)
		if err != nil {
			return fmt.Errorf("add members: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return fmt.Errorf("scan added member: %w", err)
			}
			added = append(added, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("add members: %w", err)
		}
		var total int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM conversation_members WHERE conversation_id = $1::uuid AND removed_at IS NULL`, convID).Scan(&total); err != nil {
			return fmt.Errorf("count members: %w", err)
		}
		if total > maxMembers {
			return domain.Conflict(fmt.Sprintf("A group can have at most %d members", maxMembers))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return added, nil
}

// RemoveMember soft-removes a member from a group. If that leaves members but no admin, the
// longest-standing remaining member is promoted so a group is never left without an admin.
func (s *Store) RemoveMember(ctx context.Context, convID, userID string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		typ, err := lockConversation(ctx, tx, convID)
		if err != nil {
			return err
		}
		if typ != domain.ConvGroup {
			return domain.Invalid("Members cannot be removed from direct conversations")
		}
		tag, err := tx.Exec(ctx,
			`UPDATE conversation_members SET removed_at = now()
			 WHERE conversation_id = $1::uuid AND user_id = $2::uuid AND removed_at IS NULL`, convID, userID)
		if err != nil {
			return fmt.Errorf("remove member: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return domain.NotFound()
		}
		admins, err := countAdmins(ctx, tx, convID)
		if err != nil {
			return err
		}
		if admins == 0 {
			if _, err := tx.Exec(ctx,
				`UPDATE conversation_members SET role = 'admin'
				 WHERE (conversation_id, user_id) = (
				     SELECT conversation_id, user_id FROM conversation_members
				     WHERE conversation_id = $1::uuid AND removed_at IS NULL
				     ORDER BY joined_at, user_id LIMIT 1)`, convID); err != nil {
				return fmt.Errorf("promote admin: %w", err)
			}
		}
		return nil
	})
}

// SetMemberRole changes a member's role. A group must always keep at least one admin.
func (s *Store) SetMemberRole(ctx context.Context, convID, userID, role string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		typ, err := lockConversation(ctx, tx, convID)
		if err != nil {
			return err
		}
		if typ != domain.ConvGroup {
			return domain.Invalid("Roles only apply to group conversations")
		}
		tag, err := tx.Exec(ctx,
			`UPDATE conversation_members SET role = $3
			 WHERE conversation_id = $1::uuid AND user_id = $2::uuid AND removed_at IS NULL`, convID, userID, role)
		if err != nil {
			return fmt.Errorf("set role: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return domain.NotFound()
		}
		admins, err := countAdmins(ctx, tx, convID)
		if err != nil {
			return err
		}
		if admins == 0 {
			return domain.Conflict("A group must keep at least one admin")
		}
		return nil
	})
}

// GetSettings returns conversation-level settings.
func (s *Store) GetSettings(ctx context.Context, convID string) (domain.Settings, error) {
	var st domain.Settings
	err := s.pool.QueryRow(ctx,
		`SELECT ai_enabled FROM conversation_settings WHERE conversation_id = $1::uuid`, convID).Scan(&st.AIEnabled)
	if err != nil {
		return domain.Settings{}, notFoundIfNoRows(err, "get settings")
	}
	return st, nil
}

// SetAIEnabled updates the conversation-level AI switch.
func (s *Store) SetAIEnabled(ctx context.Context, convID string, enabled bool) (domain.Settings, error) {
	var st domain.Settings
	err := s.pool.QueryRow(ctx,
		`UPDATE conversation_settings SET ai_enabled = $2 WHERE conversation_id = $1::uuid RETURNING ai_enabled`,
		convID, enabled).Scan(&st.AIEnabled)
	if err != nil {
		return domain.Settings{}, notFoundIfNoRows(err, "set ai_enabled")
	}
	return st, nil
}

// truncateRunes shortens s to at most n runes, appending an ellipsis when cut.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
