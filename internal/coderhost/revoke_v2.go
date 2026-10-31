package coderhost

import (
	"context"
	"database/sql"

	"github.com/monoes/mono-agent/internal/procbind"
)

// Every place that deletes a session's legacy token also revokes its v2 session tokens in the same
// write transaction (procbind.Revoker; design section 7, decision K1): if the v2 side cannot be written,
// the legacy delete is rolled back and the call fails. The live registry entries are dropped after the
// commit (SetBinder; nil until the host is wired).

// SetBinder sets the registry whose live entries are ended after a revocation commits.
func (s *Store) SetBinder(b *procbind.Binder) { s.binder = b }

func (s *Store) revoker() procbind.Revoker {
	return procbind.Revoker{DB: s.db, Binder: s.binder, Now: s.now}
}

var sessionKinds = []procbind.Kind{procbind.KindSession}

// revokeSessionTokens deletes the session's legacy tokens and revokes its v2 tokens.
func (s *Store) revokeSessionTokens(profileID, conversationID string) error {
	ctx := context.Background()
	return s.revoker().Run(ctx, sessionKinds, func(conn *sql.Conn) ([]procbind.Selector, error) {
		if _, err := conn.ExecContext(ctx, `DELETE FROM coder_session_tokens WHERE profile_id = ? AND conversation_id = ?`, profileID, conversationID); err != nil {
			return nil, err
		}
		return []procbind.Selector{{Profile: profileID, Session: conversationID, Kind: procbind.KindSession}}, nil
	})
}

// sweepOrphanTokens removes the legacy tokens of conversations that are gone and revokes the v2 tokens of
// those conversations, in one transaction. The orphans are read inside it, so a conversation deleted
// between the read and the delete cannot leave a token behind. It returns how many legacy rows it removed.
func (s *Store) sweepOrphanTokens() (int, error) {
	ctx := context.Background()
	n := 0
	err := s.revoker().Run(ctx, sessionKinds, func(conn *sql.Conn) ([]procbind.Selector, error) {
		var sels []procbind.Selector
		for _, q := range []string{
			`SELECT DISTINCT profile_id, conversation_id FROM coder_session_tokens t WHERE NOT EXISTS (SELECT 1 FROM ai_chat_conversations c WHERE c.id = t.conversation_id AND c.profile_id = t.profile_id)`,
			`SELECT DISTINCT profile_id, conversation_id FROM coder_session_tokens_v2 t WHERE NOT EXISTS (SELECT 1 FROM ai_chat_conversations c WHERE c.id = t.conversation_id AND c.profile_id = t.profile_id)`,
		} {
			rows, err := conn.QueryContext(ctx, q)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var p, c string
				if err := rows.Scan(&p, &c); err != nil {
					rows.Close()
					return nil, err
				}
				sels = append(sels, procbind.Selector{Profile: p, Session: c, Kind: procbind.KindSession})
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, err
			}
		}
		res, err := conn.ExecContext(ctx, `DELETE FROM coder_session_tokens WHERE NOT EXISTS (SELECT 1 FROM ai_chat_conversations c WHERE c.id = coder_session_tokens.conversation_id AND c.profile_id = coder_session_tokens.profile_id)`)
		if err != nil {
			return nil, err
		}
		c, _ := res.RowsAffected()
		n = int(c)
		return sels, nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}
