package missionpursuit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/tasks"
)

// The consent block: what the person is shown before a pursuit begins, and the digest that binds a begin to it. The digest is
// computed from host-side state only (the goal's hash, the limits, the host's switch and pin, the cost constants, the chat), never
// from a clock and never from agent text other than through the goal's own hash, so that what was shown and what is started
// cannot drift apart without the begin being refused (consent_stale).

// The blocker codes: a closed set. A blocker stops a begin before anything is written.
const (
	BlockBudgetUnknown = "budget-unknown" // the mission has no known budget
	BlockLeadUncapped  = "lead-uncapped"  // the lead's chat has no spend cap of its own
	BlockJudgePin      = "judge-pin"      // the judge may not judge this lead (same model, or the lead's model is unknown)
	BlockNoChat        = "no-chat"        // the mission has no chat to wake
	BlockHostOff       = "host-off"       // no running coder host has the pursuit loop on
	BlockNoRubric      = "no-rubric"      // the goal has no criterion the person judges
)

// BlockReasons says in plain words why each blocker stops a begin.
var BlockReasons = map[string]string{
	BlockBudgetUnknown: "the mission has no known budget, so no share of it can be measured; the pursuit would wait for you straight away",
	BlockLeadUncapped:  "the lead has no spend cap of its own; set a budget for its chat",
	BlockJudgePin:      "the judge's model is the lead's, or the lead's model is not known",
	BlockNoChat:        "the mission has no chat to wake",
	BlockHostOff:       "no running coder host has the pursuit loop on, so nothing would be judged or woken",
	BlockNoRubric:      "the goal has no rubric criterion: you are always the last judge",
}

// LiveFacts are the host-side facts a consent block is made of, read by the caller inside the same transaction as the begin.
// Cost is the canonical text of the cost rows (every figure in it comes from the loop's constants).
type LiveFacts struct {
	Cost         string
	HostOn       bool
	JudgePin     string
	WakeCapUSD   float64
	CheckerSetUp bool // the goal checker (S7): until it exists this is false; the digest covers it so a later change is stale
	BudgetKnown  bool
	LeadCapped   bool
	PinWhy       string // "" when the judge may judge this lead
}

// LiveFunc reads the LiveFacts for a mission through x (the begin's own connection).
type LiveFunc func(ctx context.Context, x tasks.Queryer, chat string, lim AwayLimits) (LiveFacts, error)

// Consent is the computed consent block of one moment: the digest and the blockers (a sorted closed set).
type Consent struct {
	Digest   string
	Blockers []string
	Facts    LiveFacts
}

// ConsentView is what `pursuit preview` shows: the goal whole, the chat, the resolved limits and the consent.
type ConsentView struct {
	Goal    Goal
	Chat    string
	Limits  AwayLimits
	Consent Consent
}

var (
	// ErrConsentStale: the host-side facts the person was shown changed (the judge, the switch, the costs, the goal's limits).
	ErrConsentStale = fmt.Errorf("%w: what you were shown has changed (the model, the checker, the costs or the host); read it again before you begin", tasks.ErrInvalid)
	// ErrChatMismatch: --chat is not the chat of the session.
	ErrChatMismatch = fmt.Errorf("%w: that is not the chat of this mission session", tasks.ErrInvalid)
	// ErrHiddenText: a start from the app shows every criterion whole, so a criterion the person cannot read in full is refused.
	ErrHiddenText = fmt.Errorf("%w: a start from the app needs criteria without hidden characters (zero-width, direction, tag or control characters) and of at most %d characters", tasks.ErrInvalid, MaxDisplayRunes)
)

// MaxDisplayRunes is the longest criterion text a start from the app takes.
const MaxDisplayRunes = 2000

// BlockedError is a begin refused for blockers; Codes are the closed codes.
type BlockedError struct{ Codes []string }

func (e *BlockedError) Error() string {
	parts := make([]string, 0, len(e.Codes))
	for _, c := range e.Codes {
		parts = append(parts, c+": "+BlockReasons[c])
	}
	return "the pursuit cannot begin: " + strings.Join(parts, "; ")
}
func (e *BlockedError) Unwrap() error { return tasks.ErrInvalid }

// IsBlocked reports whether err is a BlockedError and gives its codes.
func IsBlocked(err error) ([]string, bool) {
	var b *BlockedError
	if errors.As(err, &b) {
		return b.Codes, true
	}
	return nil, false
}

// PhraseTemplate is the shape of the phrase the person types; it is never the phrase itself.
const PhraseTemplate = "I understand: pursue mission <ID>, goal <first 8 characters of the hash>, stop at <percent> percent spent"

// Phrase is the phrase the person types to begin a pursuit from the app: it names the mission, the start of the goal's hash and
// the percent of the budget the pursuit may spend, so the person has to read the money limit.
func Phrase(session int64, goalHash string, pct int) string {
	h := goalHash
	if len(h) > 8 {
		h = h[:8]
	}
	return fmt.Sprintf("I understand: pursue mission %d, goal %s, stop at %d percent spent", session, h, pct)
}

// canonical is the encoding the digest is taken over: fixed order, one field a line, text quoted.
func canonical(session int64, chat, goalHash string, lim AwayLimits, f LiveFacts, blockers []string) string {
	var b strings.Builder
	b.WriteString("mission-pursuit-consent-v1\n")
	fmt.Fprintf(&b, "session=%d\nchat=%q\ngoal=%q\nlimits=%d,%d,%d\n", session, chat, goalHash, lim.ParkedMax, lim.Pct, lim.Wakes)
	fmt.Fprintf(&b, "host_on=%t\njudge_pin=%q\nwake_cap=%v\ncost=%q\nchecker_set_up=%t\nblockers=%q\n",
		f.HostOn, f.JudgePin, f.WakeCapUSD, f.Cost, f.CheckerSetUp, strings.Join(blockers, ","))
	return b.String()
}

// ConsentOf derives the blockers and the digest from the facts. It is pure: the preview and the begin both call it.
func ConsentOf(session int64, chat string, g Goal, lim AwayLimits, f LiveFacts) Consent {
	var bl []string
	add := func(on bool, code string) {
		if on {
			bl = append(bl, code)
		}
	}
	add(!f.BudgetKnown, BlockBudgetUnknown)
	add(!f.LeadCapped, BlockLeadUncapped)
	add(f.PinWhy != "", BlockJudgePin)
	add(chat == "", BlockNoChat)
	add(!f.HostOn, BlockHostOff)
	add(!hasRubric(g.Criteria), BlockNoRubric)
	sort.Strings(bl)
	sum := sha256.Sum256([]byte(canonical(session, chat, g.Hash, lim, f, bl)))
	return Consent{Digest: hex.EncodeToString(sum[:]), Blockers: bl, Facts: f}
}

// HiddenRune says whether a reader cannot see r or r changes how text reads (controls, format characters such as the
// direction overrides and zero-width characters, the tag block, separators, private-use and unassigned code points).
func HiddenRune(r rune) bool {
	switch {
	case unicode.IsControl(r), unicode.In(r, unicode.Cf, unicode.Co, unicode.Zl, unicode.Zp, unicode.Variation_Selector):
		return true
	case unicode.Is(unicode.Zs, r):
		return r != ' '
	}
	return !unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.P, unicode.S, unicode.Z)
}

// EscapeHidden writes each hidden character as \uXXXX (\u{XXXXX} past U+FFFF); newline and tab stay.
func EscapeHidden(s string) string {
	var b strings.Builder
	for _, r := range strings.ToValidUTF8(s, "\uFFFD") {
		switch {
		case r == '\n' || r == '\t' || !HiddenRune(r):
			b.WriteRune(r)
		case r > 0xFFFF:
			fmt.Fprintf(&b, `\u{%X}`, r)
		default:
			fmt.Fprintf(&b, `\u%04X`, r)
		}
	}
	return b.String()
}

// DisplayableWhole says whether every criterion can be shown whole to a person: no hidden character, at most MaxDisplayRunes.
func DisplayableWhole(cs []StoredCriterion) bool {
	for _, c := range cs {
		for _, t := range []string{c.Spec, c.Expected} {
			if utf8.RuneCountInString(t) > MaxDisplayRunes || !utf8.ValidString(t) {
				return false
			}
			for _, r := range t {
				if r != '\n' && r != '\t' && HiddenRune(r) {
					return false
				}
			}
		}
	}
	return true
}

// consentIn reads the live facts through x and derives the consent.
func (s *Store) consentIn(ctx context.Context, x tasks.Queryer, session int64, chat string, g Goal, lim AwayLimits, live LiveFunc) (Consent, error) {
	f, err := live(ctx, x, chat, lim)
	if err != nil {
		return Consent{}, err
	}
	return ConsentOf(session, chat, g, lim, f), nil
}

// Preview reads the consent block of a mission whose goal is proposed or confirmed, and writes nothing. A mission whose
// pursuit has started is ErrAlreadyStarted.
func (s *Store) Preview(ctx context.Context, profileID string, session int64, limits AwayLimits, live LiveFunc) (ConsentView, error) {
	lim, err := limits.resolve()
	if err != nil {
		return ConsentView{}, err
	}
	var out ConsentView
	err = s.read(ctx, func(x tasks.Queryer) error {
		chat, err := s.checkMission(ctx, x, profileID, session)
		if err != nil {
			return err
		}
		if _, err := readPursuit(ctx, x, session); err == nil {
			return ErrAlreadyStarted
		} else if !errors.Is(err, ErrNotStarted) {
			return err
		}
		g, err := readGoal(ctx, x, session)
		if err != nil {
			return err
		}
		if g.State == StateLocked {
			return ErrAlreadyStarted
		}
		c, err := s.consentIn(ctx, x, session, chat, g, lim, live)
		if err != nil {
			return err
		}
		out = ConsentView{Goal: g, Chat: chat, Limits: lim, Consent: c}
		return nil
	})
	return out, err
}

// BeginRequest is what the person was shown and agreed to: the goal's full hash, the consent digest, the chat, the limits.
// GUI says the start comes from the app, which shows every criterion whole (ErrHiddenText otherwise) and records via=gui.
type BeginRequest struct {
	GoalHash string
	Consent  string
	Chat     string
	Limits   AwayLimits
	Live     LiveFunc
	GUI      bool
}

// ConfirmAndStart confirms the goal (if it is only proposed) and starts the pursuit in ONE transaction: there is no state in
// which the goal is confirmed and the pursuit is not started because of a crash between the two. Only the operator may.
// It refuses, writing nothing: a chat other than the session's (ErrChatMismatch), a goal other than the one with GoalHash
// (ErrStale), a pursuit already started or a locked goal (ErrAlreadyStarted), blockers (BlockedError), and host-side facts that
// no longer give the consent digest the person was shown (ErrConsentStale). A confirmed goal with the same hash is not
// confirmed again.
func (s *Store) ConfirmAndStart(ctx context.Context, profileID string, session int64, req BeginRequest, by tasks.Actor) (Pursuit, Goal, error) {
	if by.Kind != tasks.Human {
		return Pursuit{}, Goal{}, operatorOnly("begin a mission's pursuit")
	}
	lim, err := req.Limits.resolve()
	if err != nil {
		return Pursuit{}, Goal{}, err
	}
	var pu Pursuit
	var goal Goal
	err = s.tx(ctx, func(x tasks.Queryer) error {
		chat, err := s.checkMission(ctx, x, profileID, session)
		if err != nil {
			return err
		}
		if req.Chat == "" || req.Chat != chat {
			return ErrChatMismatch
		}
		if _, err := readPursuit(ctx, x, session); err == nil {
			return ErrAlreadyStarted
		} else if !errors.Is(err, ErrNotStarted) {
			return err
		}
		g, err := readGoal(ctx, x, session)
		if err != nil {
			return err
		}
		if g.State == StateLocked {
			return ErrAlreadyStarted
		}
		if req.GoalHash == "" || req.GoalHash != g.Hash {
			return ErrStale
		}
		if req.GUI && !DisplayableWhole(g.Criteria) {
			return ErrHiddenText
		}
		c, err := s.consentIn(ctx, x, session, chat, g, lim, req.Live)
		if err != nil {
			return err
		}
		if len(c.Blockers) > 0 {
			return &BlockedError{Codes: c.Blockers}
		}
		if req.Consent == "" || req.Consent != c.Digest {
			return ErrConsentStale
		}
		if g.State == StateProposed {
			if err := s.confirm(ctx, x, session, by); err != nil {
				return err
			}
		}
		if err := s.insertPursuit(ctx, x, profileID, session, lim, by); err != nil {
			return err
		}
		via := "terminal"
		if req.GUI {
			via = "gui"
		}
		if _, err := x.ExecContext(ctx, `INSERT INTO task_session_events (session_id, at, actor, kind, note) VALUES (?,?,?,'note',?)`,
			session, s.stamp(), by.Label(), "pursuit begun: goal confirmed and locked, via="+via); err != nil {
			return fmt.Errorf("missionpursuit: recording the begin: %w", err)
		}
		if pu, err = readPursuit(ctx, x, session); err != nil {
			return err
		}
		goal, err = readGoal(ctx, x, session)
		return err
	})
	return pu, goal, err
}
