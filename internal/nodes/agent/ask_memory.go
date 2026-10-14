package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/monoes/mono-agent/internal/aimemory"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// defaultMemoryWindow is how many earlier messages agent.ask's memory
// option sends by default: 5 exchanges, n8n's default context window.
const defaultMemoryWindow = 10

// memoryOption is agent.ask's "memory" config: {session_key, window,
// enabled}. Present and not disabled, each item's turn sees the session's
// last window messages and is appended to the session afterwards.
type memoryOption struct {
	sessionKey string // template, resolved per item
	window     int    // messages; 0 sends the whole session
}

// parseMemoryOption reads the memory config (an object or a JSON string of
// one). nil, an empty value or enabled:false turn memory off.
func parseMemoryOption(v interface{}) (*memoryOption, error) {
	var m map[string]interface{}
	switch x := v.(type) {
	case nil:
		return nil, nil
	case map[string]interface{}:
		m = x
	case string:
		if strings.TrimSpace(x) == "" {
			return nil, nil
		}
		if err := json.Unmarshal([]byte(x), &m); err != nil {
			return nil, fmt.Errorf("memory is not a JSON object: %w", err)
		}
	default:
		return nil, fmt.Errorf("memory must be an object {session_key, window, enabled}, got %T", v)
	}
	if len(m) == 0 {
		return nil, nil
	}
	if on, ok := m["enabled"]; ok {
		if b, isBool := on.(bool); !isBool {
			return nil, fmt.Errorf("memory.enabled must be true or false")
		} else if !b {
			return nil, nil
		}
	}
	opt := &memoryOption{sessionKey: configString(m, "session_key", DefaultSessionKey), window: defaultMemoryWindow}
	switch w := m["window"].(type) {
	case nil:
	case float64:
		opt.window = int(w)
	case int:
		opt.window = w
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(w))
		if err != nil {
			return nil, fmt.Errorf("memory.window must be a number of messages, got %q", w)
		}
		opt.window = n
	default:
		return nil, fmt.Errorf("memory.window must be a number of messages, got %T", w)
	}
	if opt.window < 0 {
		return nil, fmt.Errorf("memory.window must be 0 (the whole session) or more")
	}
	return opt, nil
}

// memoryTurn is one item's use of the memory: the session is held from the
// read of its history until the turn has been appended.
type memoryTurn struct {
	profile, session string
	history          []aimemory.Message
	unlock           func()
}

// memoryStart resolves the item's session, holds it and reads its window.
func (t *asker) memoryStart(ctx context.Context, item workflow.Item) (*memoryTurn, error) {
	opt := t.cfg.memory
	if t.db == nil {
		return nil, fmt.Errorf("agent.ask: memory needs the profile database")
	}
	session, err := t.resolve(opt.sessionKey, item)
	if err != nil {
		return nil, fmt.Errorf("agent.ask: memory.session_key: %w", err)
	}
	if session = strings.TrimSpace(session); session == "" {
		return nil, fmt.Errorf("agent.ask: memory.session_key %q resolved to nothing for this item (the default reads the item's sessionId field)", opt.sessionKey)
	}
	mt := &memoryTurn{profile: vault.ProfileIDFromContext(ctx), session: session}
	mt.unlock = aimemory.LockSession(mt.profile, session)
	if mt.history, err = aimemory.Recent(ctx, t.db, mt.profile, session, opt.window); err != nil {
		mt.unlock()
		return nil, fmt.Errorf("agent.ask: %w", err)
	}
	return mt, nil
}

// wrap puts the session's earlier messages in front of the prompt.
func (mt *memoryTurn) wrap(prompt string) string {
	if len(mt.history) == 0 {
		return prompt
	}
	var b strings.Builder
	b.WriteString("Earlier messages of this conversation, oldest first:\n\n")
	for _, m := range mt.history {
		who := "User"
		switch m.Role {
		case aimemory.RoleAI:
			who = "Assistant"
		case aimemory.RoleSystem:
			who = "System"
		}
		b.WriteString(who + ": " + m.Content + "\n\n")
	}
	b.WriteString("Reply to the latest message:\n\n")
	b.WriteString(prompt)
	return b.String()
}

// finish appends the turn (the item's prompt and the answer) to the session.
func (mt *memoryTurn) finish(ctx context.Context, db *sql.DB, prompt string, answer interface{}) error {
	text, ok := answer.(string)
	if !ok {
		raw, _ := json.Marshal(answer)
		text = string(raw)
	}
	return aimemory.AppendMany(context.WithoutCancel(ctx), db, mt.profile, mt.session, []aimemory.NewMessage{
		{Role: aimemory.RoleHuman, Content: prompt}, {Role: aimemory.RoleAI, Content: text},
	})
}

func (mt *memoryTurn) release() {
	if mt != nil && mt.unlock != nil {
		mt.unlock()
		mt.unlock = nil
	}
}

// sessionOrder makes items that share a memory session run in input order
// when concurrency > 1: item i waits for waits[i] (the previous item of its
// session) and closes dones[i] when it is done. Items whose session key
// cannot be resolved here are not chained; their turn reports the error.
func (t *asker) sessionOrder(items []workflow.Item) (waits, dones []chan struct{}) {
	if t.cfg.memory == nil {
		return nil, nil
	}
	waits, dones = make([]chan struct{}, len(items)), make([]chan struct{}, len(items))
	last := map[string]chan struct{}{}
	for i, item := range items {
		dones[i] = make(chan struct{})
		key, err := t.resolve(t.cfg.memory.sessionKey, item)
		if key = strings.TrimSpace(key); err != nil || key == "" {
			continue
		}
		waits[i] = last[key]
		last[key] = dones[i]
	}
	return waits, dones
}
