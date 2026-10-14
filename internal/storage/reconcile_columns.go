package storage

import (
	"context"
	"fmt"
	"log"
)

// addedColumn is a column that code added after its table was first
// created, with ALTER TABLE ... ADD COLUMN — which SQLite cannot guard with
// IF NOT EXISTS, so no numbered migration can add it safely to databases
// where the runtime code already did.
type addedColumn struct {
	table, column, alter string
}

// addedColumns are healed by shape on every open, so a database built by
// migrations alone ends up with the same columns, in the same order, as one
// whose tables the old runtime DDL in internal/ai and internal/connections
// created and later altered. That DDL is gone: these tables now come only
// from migrations plus this list.
var addedColumns = []addedColumn{
	{"ai_chat_messages", "tool_call_id", `ALTER TABLE ai_chat_messages ADD COLUMN tool_call_id TEXT NOT NULL DEFAULT ''`},
	{"ai_chat_messages", "session_id", `ALTER TABLE ai_chat_messages ADD COLUMN session_id TEXT NOT NULL DEFAULT ''`},
	{"ai_chat_messages", "profile_id", `ALTER TABLE ai_chat_messages ADD COLUMN profile_id TEXT NOT NULL DEFAULT ''`},
	{"connections", "vault_ref", `ALTER TABLE connections ADD COLUMN vault_ref TEXT NOT NULL DEFAULT ''`},
	{"ai_chat_conversations", "mode", `ALTER TABLE ai_chat_conversations ADD COLUMN mode TEXT NOT NULL DEFAULT 'assistant'`},
	{"ai_chat_conversations", "cwd", `ALTER TABLE ai_chat_conversations ADD COLUMN cwd TEXT NOT NULL DEFAULT ''`},
	{"ai_chat_conversations", "effort", `ALTER TABLE ai_chat_conversations ADD COLUMN effort TEXT NOT NULL DEFAULT ''`},
	{"ai_chat_conversations", "org_mode", `ALTER TABLE ai_chat_conversations ADD COLUMN org_mode TEXT NOT NULL DEFAULT 'solo'`},
}

// reconcileAddedColumns adds every addedColumns entry whose table exists
// but lacks the column, each re-checked under BEGIN IMMEDIATE (another
// process may be doing the same). No-op on databases already in shape.
func (d *Database) reconcileAddedColumns(ctx context.Context) error {
	for _, c := range addedColumns {
		exists, err := d.tableExists(ctx, c.table)
		if err != nil {
			return fmt.Errorf("storage: reconcile: checking %s: %w", c.table, err)
		}
		if !exists {
			continue
		}
		has, err := d.columnExists(ctx, c.table, c.column)
		if err != nil {
			return fmt.Errorf("storage: reconcile: inspecting %s: %w", c.table, err)
		}
		if has {
			continue
		}
		if err := d.addColumnLocked(ctx, c); err != nil {
			return err
		}
	}
	return nil
}

func (d *Database) addColumnLocked(ctx context.Context, c addedColumn) error {
	conn, err := d.DB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("storage: reconcile: get conn: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("storage: reconcile: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	has, err := columnExistsOn(ctx, conn, c.table, c.column)
	if err != nil {
		return fmt.Errorf("storage: reconcile: re-inspecting %s: %w", c.table, err)
	}
	if !has {
		if _, err := conn.ExecContext(ctx, c.alter); err != nil {
			return fmt.Errorf("storage: reconcile: adding %s.%s: %w", c.table, c.column, err)
		}
		log.Printf("storage: reconcile: added %s.%s column", c.table, c.column)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("storage: reconcile: commit: %w", err)
	}
	committed = true
	return nil
}
