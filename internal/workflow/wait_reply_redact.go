package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// A reply's token can reach an agent through execution data as well as the
// waiting list RedactReplies trims: $execution.resumeUrl is an expression
// any node can use, and a node's items or error can hold the link it put in
// a message. ReplyRedactor hides those tokens in what an agent is shown:
// node items, errors, trigger data, log lines.

// ReplyTokenHidden stands in for a hidden reply token.
const ReplyTokenHidden = "[reply token hidden]"

// replyTokenLen is the length of the tokens signReply makes (43 base64url
// characters: an HMAC-SHA256, unpadded; see validReplyToken).
const replyTokenLen = 43

// maxReplyScan is the longest text String reads. A longer one could hold a
// token and would take too long to read, so it is replaced by ReplyTooLarge.
const maxReplyScan = 16 << 20

// ReplyTooLarge stands in for a text too long to scan for reply tokens.
const ReplyTooLarge = "[too large to scan for reply tokens; view it from your own terminal]"

// ReplyRedactor hides reply tokens in text shown to an agent. A token stays
// only when every reply recorded under it is one node's and marked as
// sending nothing, as its links then are in the waiting list too. A token of
// a reply that is marked, or was opened before the mark was recorded, or is
// shared (an execution's resume URL, a chat session's: replyShared), or of
// no recorded reply at all (an execution's resume URL before its core.wait
// opens) is hidden. In a link (/reply/<token>) the token is always one; a
// bare 43-character string is one when a reply is recorded under it or it is
// the resume token of an execution being shown. With no database to tell,
// every link's token and every bare 43-character base64url string is hidden.
//
// The hidden tokens of the executions shown (their replies' tokens and their
// resume tokens) are hidden by value too, in the forms tokenForms lists: a
// piece of one, reversed, hex or base64, split up by other characters. A
// text over maxReplyScan is not read: it is replaced by ReplyTooLarge.
//
// A nil *ReplyRedactor hides nothing: the view of the user's own terminal.
type ReplyRedactor struct {
	ctx context.Context
	db  *sql.DB
	ids []string // the executions shown
	// forms are the forms of the shown executions' hidden tokens, read on
	// first use (by then a run being waited for has opened its replies).
	once  sync.Once
	forms tokenForms
	mu    sync.Mutex // guards seen: a redactor may serve several goroutines
	seen  map[string]replyTokenState
}

type replyTokenState struct {
	recorded bool // a reply is recorded under the token
	hidden   bool // one of them may send, or the token is shared
}

// shown reports whether a token known to be a reply token may stay.
func (s replyTokenState) shown() bool { return s.recorded && !s.hidden }

// NewReplyRedactor hides the tokens of the replies in db (nil: every
// candidate, see ReplyRedactor). executionIDs are the executions being
// shown: their resume tokens and reply tokens are looked for by value.
func NewReplyRedactor(ctx context.Context, db *sql.DB, executionIDs ...string) *ReplyRedactor {
	return &ReplyRedactor{ctx: ctx, db: db, ids: executionIDs, seen: map[string]replyTokenState{}}
}

// known reads the shown executions' hidden tokens into r.forms, once.
func (r *ReplyRedactor) known() *tokenForms {
	r.once.Do(func() {
		var tokens []string
		for _, id := range r.ids {
			if id == "" {
				continue
			}
			if tok, err := signReply("execution", id); err == nil {
				tokens = append(tokens, tok)
			}
			if r.db == nil {
				continue
			}
			rows, err := r.db.QueryContext(r.ctx, `SELECT DISTINCT token FROM wait_replies WHERE execution_id = ?`, id)
			if err != nil {
				continue
			}
			for rows.Next() {
				var tok string
				if rows.Scan(&tok) == nil {
					tokens = append(tokens, tok)
				}
			}
			rows.Close()
		}
		for _, tok := range tokens {
			if !r.state(tok).shown() {
				r.forms.add(tok)
			}
		}
	})
	return &r.forms
}

// state looks token up once; a failed lookup counts as a marked reply.
func (r *ReplyRedactor) state(token string) replyTokenState {
	if r.db == nil {
		return replyTokenState{recorded: true, hidden: true}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.seen[token]; ok {
		return s
	}
	s := r.load(token)
	r.seen[token] = s
	return s
}

func (r *ReplyRedactor) load(token string) replyTokenState {
	failed := replyTokenState{recorded: true, hidden: true}
	rows, err := r.db.QueryContext(r.ctx, `SELECT spec FROM wait_replies WHERE token = ?`, token)
	if err != nil {
		return failed
	}
	defer rows.Close()
	var s replyTokenState
	for rows.Next() {
		var specJSON string
		if rows.Scan(&specJSON) != nil {
			return failed
		}
		var spec WaitReplySpec
		_ = json.Unmarshal([]byte(specJSON), &spec)
		s.recorded = true
		s.hidden = s.hidden || replyOutbound(spec) || replyShared(spec)
	}
	if rows.Err() != nil {
		return failed
	}
	return s
}

// String is s with its hidden reply tokens replaced by ReplyTokenHidden.
func (r *ReplyRedactor) String(s string) string {
	out, _ := r.str(s)
	return out
}

// str is String, and whether s holds a fieldPiece piece of a shown
// execution's token.
func (r *ReplyRedactor) str(s string) (string, bool) {
	out, piece := r.hideReplyTokens(s)
	if r != nil && AgentTextScrubber != nil {
		out = AgentTextScrubber(out)
	}
	return out, piece
}

func (r *ReplyRedactor) hideReplyTokens(s string) (string, bool) {
	if r == nil || len(s) < fieldPiece {
		return s, false
	}
	if len(s) > maxReplyScan {
		return ReplyTooLarge, false
	}
	s, piece := r.known().hide(r.hideLinks(s))
	return r.hideBare(s), piece
}

// hideLinks hides the token of each reply link in s (/reply/<token>,
// URL-encoded or not) that may not stay.
func (r *ReplyRedactor) hideLinks(s string) string {
	var spans [][2]int
	for i := 0; ; {
		j := strings.Index(s[i:], "reply")
		if j < 0 {
			break
		}
		j += i
		i = j + len("reply")
		if !(j >= 1 && s[j-1] == '/' || j >= 3 && s[j-3:j-1] == "%2" && (s[j-1] == 'F' || s[j-1] == 'f')) {
			continue
		}
		t := i
		switch {
		case t < len(s) && s[t] == '/':
			t++
		case t+3 <= len(s) && s[t:t+2] == "%2" && (s[t+2] == 'F' || s[t+2] == 'f'):
			t += 3
		default:
			continue
		}
		if t+replyTokenLen > len(s) || !base64urlOnly(s[t:t+replyTokenLen]) {
			continue
		}
		if !r.state(s[t : t+replyTokenLen]).shown() {
			spans = append(spans, [2]int{t, t + replyTokenLen})
		}
		i = t + replyTokenLen
	}
	return replaceSpans(s, spans)
}

// hideBare hides each run of exactly replyTokenLen base64url characters in
// s that is a hidden reply's token.
func (r *ReplyRedactor) hideBare(s string) string {
	var spans [][2]int
	for i := 0; i < len(s); {
		if !base64url(s[i]) {
			i++
			continue
		}
		j := i + 1
		for j < len(s) && base64url(s[j]) {
			j++
		}
		if j-i == replyTokenLen {
			if st := r.state(s[i:j]); st.recorded && st.hidden {
				spans = append(spans, [2]int{i, j})
			}
		}
		i = j
	}
	return replaceSpans(s, spans)
}

func base64url(c byte) bool { return alphabet[c] != 0 && c != '+' && c != '/' }

func base64urlOnly(s string) bool {
	for i := 0; i < len(s); i++ {
		if !base64url(s[i]) {
			return false
		}
	}
	return true
}

// Value is v with String applied to every string in it, map keys included.
func (r *ReplyRedactor) Value(v interface{}) interface{} {
	if r == nil {
		return v
	}
	return (&replyWalk{r: r}).value(v)
}

// replyWalk is one walk over an item: it counts the values holding a
// fieldPiece piece of a shown execution's token, and notes a list whose
// strings spread one.
type replyWalk struct {
	r      *ReplyRedactor
	pieces int
	spread bool
}

func (w *replyWalk) value(v interface{}) interface{} {
	switch t := v.(type) {
	case nil, bool, float64, json.Number:
		return v
	case string:
		s, piece := w.r.str(t)
		if piece {
			w.pieces++
		}
		return s
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, x := range t {
			out[w.r.String(k)] = w.value(x)
		}
		return out
	case []interface{}:
		w.spread = w.spread || w.r.known().spreads(t)
		out := make([]interface{}, len(t))
		for i, x := range t {
			out[i] = w.value(x)
		}
		return out
	}
	// Another Go value (a node's typed output): through its JSON form.
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	s := w.r.String(string(b))
	if s == string(b) {
		return v
	}
	var out interface{}
	if json.Unmarshal([]byte(s), &out) != nil {
		return ReplyTokenHidden
	}
	return out
}

// Map is Value for a map (nil stays nil), as one item: a shown execution's
// token spread over a list of its strings hides the map as a whole, and
// pieceFields or more of its values each holding a fieldPiece piece of one
// have those pieces hidden too.
func (r *ReplyRedactor) Map(m map[string]interface{}) map[string]interface{} {
	if r == nil || m == nil {
		return m
	}
	w := &replyWalk{r: r}
	out, _ := w.value(m).(map[string]interface{})
	switch {
	case w.spread:
		return map[string]interface{}{"note": ReplyTokenHidden + " (spread over several values)"}
	case w.pieces >= pieceFields:
		out, _ = hidePiecesIn(r.known(), out).(map[string]interface{})
	}
	return out
}

func hidePiecesIn(f *tokenForms, v interface{}) interface{} {
	switch t := v.(type) {
	case string:
		return f.hidePieces(t)
	case map[string]interface{}:
		for k, x := range t {
			t[k] = hidePiecesIn(f, x)
		}
	case []interface{}:
		for i, x := range t {
			t[i] = hidePiecesIn(f, x)
		}
	}
	return v
}

// Items is items with their reply tokens hidden, in the JSON and in the
// binary data.
func (r *ReplyRedactor) Items(items []Item) []Item {
	if r == nil || items == nil {
		return items
	}
	out := make([]Item, len(items))
	for i, it := range items {
		out[i] = Item{JSON: r.Map(it.JSON)}
		if it.Binary != nil {
			out[i].Binary = make(map[string][]byte, len(it.Binary))
			for k, b := range it.Binary {
				out[i].Binary[r.String(k)] = []byte(r.String(string(b)))
			}
		}
	}
	return out
}

// CheckReplyEdit refuses an approval's edited data (JSON) that holds
// ReplyTokenHidden: written back, the placeholder would replace the reply
// link the run needs. A field left out of the edit keeps its value.
func CheckReplyEdit(edited string) error {
	if !strings.Contains(edited, ReplyTokenHidden) {
		return nil
	}
	return fmt.Errorf("the data holds %q, which stands in for a reply link kept from you: leave out the fields that hold it and they keep their value", ReplyTokenHidden)
}

// Execution hides the reply tokens of e, in place: its error and trigger
// data, and every node's items and error.
func (r *ReplyRedactor) Execution(e *WorkflowExecution) {
	if r == nil || e == nil {
		return
	}
	e.ErrorMessage = r.String(e.ErrorMessage)
	e.TriggerData = r.Map(e.TriggerData)
	for i := range e.Nodes {
		n := &e.Nodes[i]
		n.InputItems, n.OutputItems = r.Items(n.InputItems), r.Items(n.OutputItems)
		n.ErrorMessage, n.ErrorHint = r.String(n.ErrorMessage), r.String(n.ErrorHint)
	}
}
