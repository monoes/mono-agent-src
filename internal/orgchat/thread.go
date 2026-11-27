package orgchat

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/monoes/mono-agent/internal/orgbridge"
)

// Event is one org bus event, as `org logs` and `org events` print it.
type Event struct {
	ID      string                 `json:"id"`
	TS      int64                  `json:"ts"`
	Org     string                 `json:"org"`
	Run     string                 `json:"run"`
	Type    string                 `json:"type"`
	From    string                 `json:"from"`
	To      string                 `json:"to"`
	Subject string                 `json:"subject"`
	Msg     string                 `json:"msg"`
	Reason  string                 `json:"reason"`
	Data    map[string]interface{} `json:"data"`
}

// ParseLogs reads `org logs --format json`: {items:[…]}, {events:[…]} or a
// bare array.
func ParseLogs(raw []byte) ([]Event, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var list []Event
	if raw[0] == '[' {
		err := json.Unmarshal(raw, &list)
		return list, err
	}
	var p struct {
		Items  []Event `json:"items"`
		Events []Event `json:"events"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.Items != nil {
		return p.Items, nil
	}
	return p.Events, nil
}

// Item kinds in the boss thread.
const (
	ItemHuman    = "human"    // the person's message to the org
	ItemBoss     = "boss"     // the boss's own words
	ItemQuestion = "question" // ask_human, answered inline
	ItemApproval = "approval" // a tool approval, approved or denied inline
	ItemGate     = "gate"     // a decision gate, approved or rejected inline
	ItemTeam     = "team"     // a role-to-role (or cross-org) message, a compact row
	ItemStatus   = "status"   // the org started or stopped
)

// Item is one entry of the boss thread. Pending items carry a ref the chat
// resolves them by.
type Item struct {
	ID         string `json:"id"`
	TS         int64  `json:"ts"`
	Kind       string `json:"kind"`
	Role       string `json:"role,omitempty"`
	From       string `json:"from,omitempty"`
	To         string `json:"to,omitempty"`
	Subject    string `json:"subject,omitempty"`
	Text       string `json:"text,omitempty"`
	Ref        string `json:"ref,omitempty"`
	Action     string `json:"action,omitempty"`
	Name       string `json:"name,omitempty"`
	External   bool   `json:"external,omitempty"`
	Pending    bool   `json:"pending"`
	Resolution string `json:"resolution,omitempty"`
	ResolvedBy string `json:"resolved_by,omitempty"`
	Answer     string `json:"answer,omitempty"`
	// Blocking: a question that holds the org back until it is closed.
	Blocking bool `json:"blocking,omitempty"`
	// An approval of a shell call: the command (CommandTruncated when only its
	// start is known) and the role's folder, which `org chat history` fills in.
	Command          string `json:"command,omitempty"`
	CommandTruncated bool   `json:"command_truncated,omitempty"`
	Cwd              string `json:"cwd,omitempty"`
	// An approval's request id, empty when monomind recorded none (then it
	// can't be approved on its own), and the call's arguments as monomind
	// recorded them, redacted and cut.
	RequestID string                 `json:"request_id,omitempty"`
	Input     map[string]interface{} `json:"input,omitempty"`
}

var approvalStatus = regexp.MustCompile(`^Approval (granted|denied) for (.+)$`)

// split separates an address into its org and role; a bare role is local.
func split(addr string) (org, role string) {
	if i := strings.IndexByte(addr, ':'); i >= 0 {
		return addr[:i], addr[i+1:]
	}
	return "", addr
}

// isHuman reports whether an address is the person: "human" or human:<who>.
func isHuman(addr string) bool {
	o, r := split(addr)
	return o == "human" || (o == "" && r == "human")
}

// local resolves an address to a role of org, or "".
func local(org, addr string) string {
	o, r := split(addr)
	if o == "" || o == org {
		return r
	}
	return ""
}

func str(m map[string]interface{}, k string) string {
	if s, ok := m[k].(string); ok {
		return s
	}
	return ""
}

type builder struct {
	org, boss string
	items     []Item
	byRef     map[string]int // kind+ref → index
	approval  map[string]int // role:action → index of its latest approval item
	// unnamed is, per role:action, the approval items of bus events that
	// carried no request id, in bus order.
	unnamed map[string][]int
	seen    map[string]bool
	// open is the boss item the next boss chat event continues, or -1. A
	// streaming runtime (e.g. vercel/openrouter) puts every text delta on
	// the bus as its own chat event (#294); consecutive ones are one reply.
	open int
}

func (b *builder) add(it Item) int {
	b.items = append(b.items, it)
	return len(b.items) - 1
}

func (b *builder) resolve(kind, ref, state, by string) {
	i, ok := b.byRef[kind+"\x00"+ref]
	if !ok {
		return
	}
	b.items[i].Pending = false
	b.items[i].Resolution = state
	if by != "" {
		b.items[i].ResolvedBy = by
	}
}

func (b *builder) event(e Event) {
	if e.ID != "" {
		if b.seen[e.ID] {
			return
		}
		b.seen[e.ID] = true
	}
	// Only more boss text, or a role's working/idle state flipping mid
	// stream, keeps the boss reply open; anything else (a tool call, a
	// question, a gate, a message, usage at the end of the turn) ends it.
	bossChat := e.Type == "chat" && b.boss != "" && e.From == b.boss
	if !bossChat && !(e.Type == "status" && e.Reason == "state-change") {
		b.open = -1
	}
	d := e.Data
	switch e.Type {
	case "xorg", "message":
		// A cross-org message lands on both buses: one copy per message id.
		if id := str(d, "messageId"); id != "" {
			if b.seen["msg\x00"+id] {
				return
			}
			b.seen["msg\x00"+id] = true
		}
		text := orgbridge.StripTrace(e.Msg)
		if isHuman(e.From) {
			if to := local(b.org, e.To); to != "" {
				b.add(Item{ID: e.ID, TS: e.TS, Kind: ItemHuman, From: e.From, To: to, Subject: e.Subject, Text: text})
			}
			return
		}
		if isHuman(e.To) {
			return
		}
		from, to := local(b.org, e.From), local(b.org, e.To)
		it := Item{ID: e.ID, TS: e.TS, Kind: ItemTeam, From: e.From, To: e.To, Subject: e.Subject, Text: text}
		if from != "" && to != "" {
			it.From, it.To = from, to
		} else {
			it.External = true
		}
		b.add(it)
	case "chat":
		if !bossChat {
			return
		}
		if b.open >= 0 {
			b.items[b.open].Text += e.Msg
			return
		}
		if strings.TrimSpace(e.Msg) != "" {
			b.open = b.add(Item{ID: e.ID, TS: e.TS, Kind: ItemBoss, Role: e.From, Text: e.Msg})
		}
	case "question":
		if qid := str(d, "questionId"); qid != "" {
			if _, dup := b.byRef[ItemQuestion+"\x00"+qid]; dup {
				return
			}
			b.byRef[ItemQuestion+"\x00"+qid] = b.add(Item{ID: e.ID, TS: e.TS, Kind: ItemQuestion, Role: e.From, Ref: qid, Text: str(d, "question"), Pending: true, Blocking: d["blocking"] == true})
			return
		}
		if action := str(d, "action"); action != "" {
			requestID := str(d, "requestId")
			ref := requestID
			if ref == "" {
				ref = e.From + ":" + action
			}
			input, _ := d["input"].(map[string]interface{})
			command, truncated := Command(input)
			i := b.add(Item{ID: e.ID, TS: e.TS, Kind: ItemApproval, Role: e.From, Ref: ref, Action: action, Text: str(d, "question"), Pending: true,
				Command: command, CommandTruncated: truncated, RequestID: requestID, Input: input})
			b.byRef[ItemApproval+"\x00"+ref] = i
			b.approval[e.From+":"+action] = i
			if requestID == "" {
				b.unnamed[e.From+":"+action] = append(b.unnamed[e.From+":"+action], i)
			}
		}
	case "gate":
		gid := str(d, "gateId")
		if gid == "" {
			return
		}
		switch e.Reason {
		case "gate-approved":
			b.resolve(ItemGate, gid, StateApproved, "")
		case "gate-rejected":
			b.resolve(ItemGate, gid, StateRejected, "")
		default:
			if _, dup := b.byRef[ItemGate+"\x00"+gid]; dup {
				return
			}
			b.byRef[ItemGate+"\x00"+gid] = b.add(Item{ID: e.ID, TS: e.TS, Kind: ItemGate, Role: e.From, Ref: gid, Name: str(d, "name"), Text: str(d, "description"), Pending: true})
		}
	case "status":
		switch {
		case e.Msg == "question answered":
			b.resolve(ItemQuestion, str(d, "questionId"), StateAnswered, "")
		case e.From == "" && (strings.HasPrefix(e.Msg, "org started") || e.Msg == "org stopped"):
			b.add(Item{ID: e.ID, TS: e.TS, Kind: ItemStatus, Text: e.Msg})
		default:
			if m := approvalStatus.FindStringSubmatch(e.Msg); m != nil {
				if i, ok := b.approval[e.From+":"+m[2]]; ok && b.items[i].Pending {
					b.items[i].Pending = false
					b.items[i].Resolution = map[string]string{"granted": StateApproved, "denied": StateDenied}[m[1]]
				}
			}
		}
	case "audit":
		if e.Reason != "decision-resolved" {
			return
		}
		kind, ref, verdict, by := str(d, "kind"), str(d, "ref"), str(d, "verdict"), str(d, "resolver")
		switch kind {
		case "question":
			state := StateAnswered
			if verdict == StateDismissed {
				state = StateDismissed
			}
			b.resolve(ItemQuestion, ref, state, by)
		case "gate":
			state := StateRejected
			if verdict == "approved" {
				state = StateApproved
			}
			b.resolve(ItemGate, ref, state, by)
		case "approval":
			state := StateDenied
			if verdict == "approved" {
				state = StateApproved
			}
			b.resolve(ItemApproval, ref, state, by)
		}
	}
}

// merge folds in what the org's human item lists say: full text, whether an
// item is still pending, and the answer. Pending items the bus log doesn't
// show (asked in an earlier run) are added, so they can still be answered.
func (b *builder) merge(h HumanItems) {
	for _, q := range h.Questions {
		i, ok := b.byRef[ItemQuestion+"\x00"+q.QuestionID]
		if !ok {
			if q.Answer != nil {
				continue
			}
			i = b.add(Item{ID: q.QuestionID, TS: q.TS, Kind: ItemQuestion, Role: q.Role, Ref: q.QuestionID})
			b.byRef[ItemQuestion+"\x00"+q.QuestionID] = i
		}
		it := &b.items[i]
		if q.Question != "" {
			it.Text = q.Question
		}
		if q.Blocking != nil {
			it.Blocking = *q.Blocking
		}
		it.Pending = q.Answer == nil && q.State != StateDismissed
		if q.State == StateDismissed {
			it.Resolution = StateDismissed
		} else if q.Answer != nil {
			it.Resolution = StateAnswered
			it.Answer = *q.Answer
		}
	}
	b.mergeApprovals(h.Approvals)
	for _, g := range h.Gates {
		i, ok := b.byRef[ItemGate+"\x00"+g.ID]
		if !ok {
			if gateState(g) != "" {
				continue
			}
			i = b.add(Item{ID: g.ID, TS: g.CreatedAt, Kind: ItemGate, Role: g.RoleID, Ref: g.ID, Name: g.Name})
			b.byRef[ItemGate+"\x00"+g.ID] = i
		}
		it := &b.items[i]
		if g.Description != "" {
			it.Text = g.Description
		}
		if g.Name != "" {
			it.Name = g.Name
		}
		it.Pending = gateState(g) == ""
		it.Resolution = gateState(g)
		if g.ResolvedBy != "" {
			it.ResolvedBy = g.ResolvedBy
		}
	}
}

// BuildThread is the boss thread of org: the person's messages, the boss's
// words, questions, approvals and gates (with whether each is still
// pending), role-to-role messages as rows, and the org starting and
// stopping, in bus order. It is a pure function of its inputs. limit keeps
// the newest items (0 = all); pending items are always kept.
func BuildThread(org, boss string, events []Event, human HumanItems, limit int) []Item {
	b := &builder{org: org, boss: boss, items: []Item{}, byRef: map[string]int{}, approval: map[string]int{}, unnamed: map[string][]int{}, seen: map[string]bool{}, open: -1}
	for _, e := range events {
		b.event(e)
	}
	b.merge(human)
	items := b.items
	if limit <= 0 || len(items) <= limit {
		return items
	}
	keep := make([]bool, len(items))
	n := 0
	for i := len(items) - 1; i >= 0; i-- {
		if n < limit {
			keep[i] = true
			n++
		} else if items[i].Pending {
			keep[i] = true
		}
	}
	out := make([]Item, 0, limit)
	for i, it := range items {
		if keep[i] {
			out = append(out, it)
		}
	}
	return out
}
