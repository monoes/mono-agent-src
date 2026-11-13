package missionpursuit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
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
	// BlockHostInfoInvalid: what the host published (host.json) is not a usable pin or wake-up cap. The file is display-only: any
	// process running as the user can write it, so a value that cannot be real is never shown as a fact.
	BlockHostInfoInvalid = "host-info-invalid"
)

// BlockReasons says in plain words why each blocker stops a begin.
var BlockReasons = map[string]string{
	BlockBudgetUnknown:   "the mission has no known budget, so no share of it can be measured; the pursuit would wait for you straight away",
	BlockLeadUncapped:    "the lead has no spend cap of its own; set a budget for its chat",
	BlockJudgePin:        "the judge's model is the lead's, or the lead's model is not known",
	BlockNoChat:          "the mission has no chat to wake",
	BlockHostOff:         "no running coder host has the pursuit loop on, so nothing would be judged or woken",
	BlockNoRubric:        "the goal has no rubric criterion: you are always the last judge",
	BlockHostInfoInvalid: "what the host published (its judge model or wake-up cap) is not a usable value, so it cannot be shown to you as a fact",
}

// LiveFacts are the host-side facts a consent block is made of, read by the caller inside the same transaction as the begin.
// Cost is the canonical text of the cost rows (every figure in it comes from the loop's constants).
type LiveFacts struct {
	Cost        string
	HostOn      bool
	HostInvalid bool // the host's published pin or cap failed validation (BlockHostInfoInvalid); they are then not part of the facts
	JudgePin    string
	WakeCapUSD  float64
	// ReportedWakeCapUSD is what the host's file says (0 = none): shown beside the bound, never an input of the digest or the sums.
	ReportedWakeCapUSD float64
	CheckerSetUp       bool // the goal checker (S7): until it exists this is false; the digest covers it so a later change is stale
	BudgetKnown        bool
	LeadCapped         bool
	PinWhy             string // "" when the judge may judge this lead
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
	// ErrHiddenText: a start from the app shows every criterion whole, so a criterion with a character a reader cannot see is refused.
	ErrHiddenText = fmt.Errorf("%w: this criterion contains a character the app can't show safely (zero-width, direction, tag, control or invisible filler characters, emoji variation selectors and joiners); re-propose it without it", tasks.ErrInvalid)
	// ErrCriterionTooLong: a start from the app shows every criterion whole, so one over MaxDisplayRunes characters is refused.
	ErrCriterionTooLong = fmt.Errorf("%w: a start from the app takes criteria of at most %d characters each; re-propose it shorter", tasks.ErrInvalid, MaxDisplayRunes)
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
const PhraseTemplate = "I understand: pursue mission <ID>, goal <first 8 characters of the hash>, stop at <percent> percent spent, <parked> waiting questions or <wakes> wake-ups"

// Phrase is the phrase the person types to begin a pursuit from the app: it names the mission, the start of the goal's hash and
// all three away limits (the percent of the budget, the waiting questions, the wake-ups of the lead), so the person has to read
// the limits that bound the money. lim must be resolved (Resolved).
func Phrase(session int64, goalHash string, lim AwayLimits) string {
	h := goalHash
	if len(h) > 8 {
		h = h[:8]
	}
	return fmt.Sprintf("I understand: pursue mission %d, goal %s, stop at %d percent spent, %d waiting questions or %d wake-ups", session, h, lim.Pct, lim.ParkedMax, lim.Wakes)
}

// Resolved is the limits with the defaults filled in, or an error for a value out of range.
func (l AwayLimits) Resolved() (AwayLimits, error) { return l.resolve() }

// BudgetKnown is the loop's predicate for a known budget: the budget is a positive finite number and the spend a finite number
// of at least 0. Anything else (NaN, an infinity, zero, a negative) is unknown, never unlimited.
func BudgetKnown(budget, spent float64) bool {
	fin := func(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }
	return fin(budget) && fin(spent) && budget > 0 && spent >= 0
}

// canonical is the encoding the digest is taken over: fixed order, one field a line, text quoted.
func canonical(session int64, chat, goalHash string, lim AwayLimits, f LiveFacts, blockers []string) string {
	var b strings.Builder
	b.WriteString("mission-pursuit-consent-v1\n")
	fmt.Fprintf(&b, "session=%d\nchat=%q\ngoal=%q\nlimits=%d,%d,%d\n", session, chat, goalHash, lim.ParkedMax, lim.Pct, lim.Wakes)
	fmt.Fprintf(&b, "host_invalid=%t\n", f.HostInvalid)
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
	add(f.HostInvalid, BlockHostInfoInvalid)
	add(!hasRubric(g.Criteria), BlockNoRubric)
	sort.Strings(bl)
	sum := sha256.Sum256([]byte(canonical(session, chat, g.Hash, lim, f, bl)))
	return Consent{Digest: hex.EncodeToString(sum[:]), Blockers: bl, Facts: f}
}

// HiddenRune says whether a reader cannot see r or r changes how text reads (controls, format characters such as the
// direction overrides and zero-width characters, the tag block, separators, private-use and unassigned code points).
func HiddenRune(r rune) bool {
	switch r {
	case 0x034F, 0x115F, 0x1160, 0x17B4, 0x17B5, 0x2800, 0x3164, 0xFFA0: // blank-looking fillers and padding that no category above catches
		return true
	}
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

// CheckDisplayable says whether every criterion can be shown whole to a person: ErrCriterionTooLong for a text over
// MaxDisplayRunes, ErrHiddenText for a character a reader does not see (this includes emoji variation selectors and joiners:
// they are refused rather than guessed at, and the criterion is proposed again without them).
func CheckDisplayable(cs []StoredCriterion) error {
	var hidden bool
	for _, c := range cs {
		for _, t := range []string{c.Spec, c.Expected} {
			if utf8.RuneCountInString(t) > MaxDisplayRunes {
				return ErrCriterionTooLong
			}
			if !utf8.ValidString(t) {
				hidden = true
			}
			for _, r := range t {
				if r != '\n' && r != '\t' && HiddenRune(r) {
					hidden = true
				}
			}
		}
	}
	if hidden {
		return ErrHiddenText
	}
	return nil
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
		if req.GUI {
			if err := CheckDisplayable(g.Criteria); err != nil {
				return err
			}
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
