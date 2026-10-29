package chat

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/profiledir"
)

// `chat history list --all`: the conversations of the profile and its org runs
// in one list, newest first, for the GUI's history. The per-conversation list
// stays what it was; --all only adds the title, the status of the latest turn,
// the bound AI task and the org runs.

// histApplicationAny as --application means "has an application link".
const histApplicationAny = "any"

// Statuses of a history row.
const (
	histRunning = "running"
	histDone    = "done"
	histError   = "error"
	histNeeds   = "needs"
	histIdle    = "idle"
)

const (
	histKindChat = "chat"
	histKindOrg  = "org"
	// histTitleMax is the length of a title taken from the first message.
	histTitleMax = 60
	// histPromptBytes bounds the first message read per row.
	histPromptBytes = 400
)

// HistoryRow is one row of the unified history.
type HistoryRow struct {
	ID              string   `json:"id"`
	Kind            string   `json:"kind"`
	Title           string   `json:"title"`
	Renamed         bool     `json:"renamed,omitempty"` // the title is one the person typed
	Status          string   `json:"status"`
	UpdatedAt       string   `json:"updatedAt"`
	Runtime         string   `json:"runtime,omitempty"`
	Model           string   `json:"model,omitempty"`
	Effort          string   `json:"effort,omitempty"`
	Access          string   `json:"access,omitempty"`
	Mode            string   `json:"mode,omitempty"`
	OrgMode         string   `json:"orgMode,omitempty"`
	Cwd             string   `json:"cwd,omitempty"`
	WorkflowContext string   `json:"workflowContext,omitempty"`
	TaskID          string   `json:"taskId,omitempty"`
	TaskStatus      string   `json:"taskStatus,omitempty"`
	TaskGoal        string   `json:"taskGoal,omitempty"`
	ApplicationID   string   `json:"applicationId,omitempty"`
	OrgName         string   `json:"orgName,omitempty"`
	RunID           string   `json:"runId,omitempty"`
	CostUSD         *float64 `json:"costUsd,omitempty"`
}

type historyPage struct {
	Items      []HistoryRow `json:"items"`
	NextCursor string       `json:"next_cursor"`
	Warnings   []string     `json:"warnings"`
}

type historyFilter struct {
	cursor, kind, status, application, query, id string
	limit                                        int
}

// sortKey is what the cursor compares: updated_at|id, as ListConversations.
func (r HistoryRow) sortKey() string { return r.UpdatedAt + "|" + r.ID }

// withListAll adds --all (and its filters) to `chat history list`.
func withListAll(cfg *Deps, cmd *cobra.Command) *cobra.Command {
	var all bool
	var f historyFilter
	plain := cmd.RunE
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if !all {
			for _, name := range []string{"kind", "status", "application", "query", "id"} {
				if c.Flags().Changed(name) {
					return clicore.InvalidInput("--%s needs --all", name)
				}
			}
			return plain(c, args)
		}
		// --cursor and --limit are the plain list's own flags.
		f.limit, _ = c.Flags().GetInt("limit")
		n, err := chatLimit(f.limit, 50, 200)
		if err != nil {
			return err
		}
		f.limit = n
		if f.kind != "" && f.kind != histKindChat && f.kind != histKindOrg {
			return clicore.InvalidInput("--kind must be chat or org")
		}
		switch f.status {
		case "", histRunning, histDone, histError, histNeeds, histIdle:
		default:
			return clicore.InvalidInput("--status must be running, done, error, needs or idle")
		}
		page, err := listHistory(c, cfg, f)
		if err != nil {
			return err
		}
		if cfg.JSONOutput() {
			return clicore.PrintJSON(page)
		}
		for _, r := range page.Items {
			fmt.Printf("%-8s %-8s %s  %s\n", r.Kind, r.Status, r.UpdatedAt, r.Title)
		}
		for _, w := range page.Warnings {
			fmt.Printf("warning: %s\n", w)
		}
		if page.NextCursor != "" {
			fmt.Printf("more: --cursor %q\n", page.NextCursor)
		}
		return nil
	}
	fl := cmd.Flags()
	fl.BoolVar(&all, "all", false, "One row per conversation and org run, with title, status and the bound AI task")
	fl.StringVar(&f.kind, "kind", "", "With --all: only chat or org rows")
	fl.StringVar(&f.status, "status", "", "With --all: only rows that are running, done, error, needs or idle")
	fl.StringVar(&f.application, "application", "", "With --all: only conversations whose AI task belongs to this application id, or to any application with \"any\"")
	fl.StringVar(&f.id, "id", "", "With --all: only this conversation (no org rows)")
	fl.StringVar(&f.query, "query", "", "With --all: only rows whose title, folder or model contains this text")
	return cmd
}

func listHistory(cmd *cobra.Command, cfg *Deps, f historyFilter) (historyPage, error) {
	f.cursor, _ = cmd.Flags().GetString("cursor")
	page := historyPage{Items: []HistoryRow{}, Warnings: []string{}}
	db, err := cfg.OpenDB()
	if err != nil {
		return page, fmt.Errorf("initializing database: %w", err)
	}
	defer db.Close()
	profileID := cfg.ProfileID()

	var rows []HistoryRow
	if f.kind != histKindOrg {
		if rows, err = queryHistoryRows(db.DB, profileID, f, f.limit+1); err != nil {
			return page, err
		}
	}
	if f.kind != histKindChat && f.application == "" && f.id == "" {
		orgs, warns := orgHistoryRows(cmd.Context(), profiledir.Root(db.DB, profileID), f)
		rows = append(rows, orgs...)
		page.Warnings = append(page.Warnings, warns...)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].sortKey() > rows[j].sortKey() })
	if len(rows) > f.limit {
		page.NextCursor = rows[f.limit-1].sortKey()
		rows = rows[:f.limit]
	}
	page.Items = append(page.Items, rows...)
	return page, nil
}

// historyQuerier is what queryHistoryRows needs of the database.
type historyQuerier interface {
	Query(q string, args ...any) (*sql.Rows, error)
}

// histSQL reads every row in ONE statement: the latest turn, the first turn
// (its text titles the chat) and the latest bound AI task come in as joins on
// indexed lookups, so a page costs one query however many rows it holds.
const histSQL = `SELECT id, workflow_context, runtime_id, model, mode, cwd, updated_at, effort, org_mode, access,
	status, first_prompt, task_id, task_status, task_goal, application_id, custom_title FROM (
SELECT c.id, c.workflow_context, c.runtime_id, c.model, c.mode, c.cwd, c.updated_at, c.effort, c.org_mode, c.access,
	CASE
		WHEN COALESCE(k.status, '') = 'needs_user' THEN 'needs'
		WHEN lt.status = 'active' THEN 'running'
		WHEN lt.status = 'completed' THEN 'done'
		WHEN lt.status IN ('failed', 'interrupted') THEN 'error'
		ELSE 'idle'
	END AS status,
	COALESCE(substr(ft.prompt, 1, ` + "%d" + `), '') AS first_prompt,
	COALESCE(k.id, '') AS task_id, COALESCE(k.status, '') AS task_status,
	COALESCE(k.goal, '') AS task_goal, COALESCE(k.application_id, '') AS application_id,
	c.title AS custom_title
FROM ai_chat_conversations c
LEFT JOIN ai_chat_turns lt ON lt.id = (SELECT id FROM ai_chat_turns WHERE conversation_id = c.id AND profile_id = c.profile_id ORDER BY created_at DESC, id DESC LIMIT 1)
LEFT JOIN ai_chat_turns ft ON ft.id = (SELECT id FROM ai_chat_turns WHERE conversation_id = c.id AND profile_id = c.profile_id ORDER BY created_at ASC, id ASC LIMIT 1)
LEFT JOIN ai_tasks k ON k.id = (SELECT id FROM ai_tasks WHERE conversation_id = c.id AND profile_id = c.profile_id ORDER BY created_at DESC, id DESC LIMIT 1)
WHERE c.profile_id = ? AND c.backend = 'agent'%s
) WHERE 1 = 1%s ORDER BY updated_at DESC, id DESC LIMIT ?`

func queryHistoryRows(db historyQuerier, profileID string, f historyFilter, limit int) ([]HistoryRow, error) {
	args := []any{profileID}
	inner, outer := "", ""
	if f.cursor != "" {
		inner += ` AND c.updated_at || '|' || c.id < ?`
		args = append(args, f.cursor)
	}
	if f.id != "" {
		inner += ` AND c.id = ?`
		args = append(args, f.id)
	}
	if f.application == histApplicationAny {
		outer += ` AND application_id <> ''`
	} else if f.application != "" {
		outer += ` AND application_id = ?`
		args = append(args, f.application)
	}
	if f.status != "" {
		outer += ` AND status = ?`
		args = append(args, f.status)
	}
	if f.query != "" {
		// Titles are derived in Go from these columns, so the text is matched
		// on them (the first message is bounded to its first bytes).
		outer += ` AND (LOWER(task_goal) LIKE ? ESCAPE '\' OR LOWER(first_prompt) LIKE ? ESCAPE '\' OR LOWER(cwd) LIKE ? ESCAPE '\' OR LOWER(model) LIKE ? ESCAPE '\' OR LOWER(custom_title) LIKE ? ESCAPE '\')`
		like := "%" + likeEscape(strings.ToLower(f.query)) + "%"
		args = append(args, like, like, like, like, like)
	}
	args = append(args, limit)
	rows, err := db.Query(fmt.Sprintf(histSQL, histPromptBytes, inner, outer), args...)
	if err != nil {
		return nil, fmt.Errorf("list history: %w", err)
	}
	defer rows.Close()
	out := []HistoryRow{}
	for rows.Next() {
		var r HistoryRow
		var first, custom string
		if err := rows.Scan(&r.ID, &r.WorkflowContext, &r.Runtime, &r.Model, &r.Mode, &r.Cwd, &r.UpdatedAt, &r.Effort, &r.OrgMode, &r.Access,
			&r.Status, &first, &r.TaskID, &r.TaskStatus, &r.TaskGoal, &r.ApplicationID, &custom); err != nil {
			return nil, fmt.Errorf("scan history: %w", err)
		}
		r.Kind = histKindChat
		r.TaskGoal = redactTitleText(r.TaskGoal)
		r.Title = historyTitle(r.TaskGoal, first, r.Cwd)
		if c := oneLine(redactTitleText(custom)); c != "" {
			r.Title, r.Renamed = c, true
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// chatDisplayTitle is the title the app shows for one conversation ("" when it has none yet, or
// the row cannot be read): the name the person typed, else the history's derived one.
func chatDisplayTitle(db historyQuerier, profileID, id string) string {
	rows, err := queryHistoryRows(db, profileID, historyFilter{id: id}, 1)
	if err != nil || len(rows) == 0 || rows[0].Title == histNewChat {
		return ""
	}
	return rows[0].Title
}

// histNewChat is the title of a chat with nothing to title it by.
const histNewChat = "New chat"

// historyTitle is the task goal when a task is bound, else the first message
// trimmed to histTitleMax, else the folder's name, else "New chat". Both texts
// are the user's own and go through the redaction first.
func historyTitle(goal, firstPrompt, cwd string) string {
	if g := oneLine(goal); g != "" {
		return truncateRunes(g, 120)
	}
	if p := oneLine(redactTitleText(firstPrompt)); p != "" {
		return truncateRunes(p, histTitleMax)
	}
	if cwd != "" {
		if b := filepath.Base(cwd); b != "." && b != string(filepath.Separator) {
			return b
		}
	}
	return histNewChat
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}
