// wails-app: what the app reads out of the CLI's pursuit preview, and the words of the native confirmation built from it.
//
// The preview document is the CLI's (`task session pursuit preview --json`). It is read strictly: a closed shape, no
// repeated or case-variant key, a hash and a digest of 64 hex characters, plausible numbers, closed blocker codes, text
// cleaned of hidden characters. Anything the app cannot read plainly is an error and no grant is issued, so the dialog never
// describes a preview it did not understand. The app holds no dollar figure, no limit default and no copy of the phrase: the
// money is the CLI's cost rows, the phrase is the CLI's template filled with the values the CLI resolved.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The sanity bounds of what is read: not the CLI's ranges (the CLI decides those), only what cannot be real.
const (
	pursuitSane          = 1_000_000 // any count or limit above this is not a number the CLI printed
	pursuitMaxCriteria   = 256
	pursuitDocMax        = 1 << 20
	pursuitPhraseMax     = 400
	pursuitChatMax       = 128
	pursuitCostTextRunes = 512
	pursuitShortRunes    = 120
	pursuitGoalRunes     = 2000 // the goal in the dialog: whole, or the preview is refused (errGoalTooLong)
	pursuitMarkRun       = 4    // most combining marks in a row
)

var (
	blockerCodeRe = regexp.MustCompile(`^[a-z]+(-[a-z]+)*$`)
	// the placeholders of the CLI's phrase template; each must occur exactly once
	phrasePlaceholders = []string{"<ID>", "<first 8 characters of the hash>", "<percent>", "<parked>", "<wakes>"}
	pursuitTopKeys     = []string{"schema_version", "profile", "session_id", "chat_id", "goal", "limits", "rubric_present", "cost", "checker", "host",
		"blockers", "phrase_template", "consent_digest"}
	// the three rows of the CLI's cost table, in the order the dialog says them
	pursuitCostKeys = []string{"mission_hour", "host_hour", "until_stop"}
)

type pursuitLimits struct {
	ParkedMax int `json:"parked_max"`
	Pct       int `json:"pct"`
	WakesMax  int `json:"wakes_max"`
}

// PursuitLimits are the away limits the page sends: 0 is "the CLI's default" for a preview; a start names all three, as the
// preview resolved them.
type PursuitLimits struct {
	ParkedMax int `json:"parkedMax"`
	Pct       int `json:"pct"`
	WakesMax  int `json:"wakesMax"`
}

type pursuitCriterion struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Spec     string `json:"spec"`
	Expected string `json:"expected"`
}

// pursuitFacts are what the preview said, as far as the confirmation needs them.
type pursuitFacts struct {
	session  int64
	chat     string
	hash     string
	digest   string
	docSum   [32]byte
	limits   pursuitLimits
	template string
	loopOn   bool
	pin      string
	setUp    bool
	blockers []string
	cost     []string // the CLI's three cost rows, cleaned
	criteria []pursuitCriterion
}

type pursuitPreviewDoc struct {
	SchemaVersion int             `json:"schema_version"`
	Profile       json.RawMessage `json:"profile"`
	SessionID     int64           `json:"session_id"`
	ChatID        string          `json:"chat_id"`
	Goal          struct {
		State    string             `json:"state"`
		Hash     string             `json:"hash"`
		Criteria []pursuitCriterion `json:"criteria"`
	} `json:"goal"`
	Limits        pursuitLimits `json:"limits"`
	RubricPresent bool          `json:"rubric_present"`
	Cost          struct {
		Rows []struct {
			Key   string  `json:"key"`
			USD   float64 `json:"usd"`
			Known bool    `json:"known"`
			Text  string  `json:"text"`
		} `json:"rows"`
		Text string `json:"text"`
	} `json:"cost"`
	Checker struct {
		SetUp bool `json:"set_up"`
	} `json:"checker"`
	Host struct {
		LoopOn          bool    `json:"loop_on"`
		JudgePin        string  `json:"judge_pin"`
		WakeCapUSD      float64 `json:"wake_cap_usd"`
		JudgeCallCapUSD float64 `json:"judge_call_cap_usd"`
	} `json:"host"`
	Blockers []struct {
		Code   string `json:"code"`
		Reason string `json:"reason"`
	} `json:"blockers"`
	PhraseTemplate string `json:"phrase_template"`
	ConsentDigest  string `json:"consent_digest"`
}

// foldKey is s under Unicode simple case folding, the equivalence encoding/json uses to match a key to a field (so the
// long s and "s" are one key).
func foldKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		m := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			if f < m {
				m = f
			}
		}
		b.WriteRune(m)
	}
	return b.String()
}

// noAmbiguousKeys refuses a JSON document that has, anywhere, an object with two keys that are the same once case is
// ignored (encoding/json would pick one by its own rule, and a reader of the page another).
func noAmbiguousKeys(doc []byte) error {
	dec := json.NewDecoder(bytes.NewReader(doc))
	var walk func() error
	walk = func() error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		d, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		seen := map[string]bool{}
		for dec.More() {
			if d == '{' {
				k, err := dec.Token()
				if err != nil {
					return err
				}
				low := foldKey(k.(string))
				if seen[low] {
					return errors.New("the preview repeats a key")
				}
				seen[low] = true
			}
			if err := walk(); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("the preview is more than one document")
	}
	return nil
}

// pursuitFactsOf reads the facts out of the CLI's preview document for session. Anything it cannot read plainly is an error.
func pursuitFactsOf(doc []byte, session int64, asked PursuitLimits) (pursuitFacts, error) {
	bad := func(why string) (pursuitFacts, error) {
		return pursuitFacts{}, errors.New("the preview could not be read: " + why + "; nothing can be started from it")
	}
	if len(doc) == 0 || len(doc) > pursuitDocMax || !utf8.Valid(doc) {
		return bad("it is empty, too large or not text")
	}
	if err := noAmbiguousKeys(doc); err != nil {
		return bad(err.Error())
	}
	var d pursuitPreviewDoc
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return bad("its shape is not the one this app knows")
	}
	var keys map[string]json.RawMessage
	_ = json.Unmarshal(doc, &keys)
	for _, k := range pursuitTopKeys {
		if _, ok := keys[k]; !ok {
			return bad("a field is missing: " + k)
		}
	}
	if d.SessionID != session {
		return bad("it is for another session")
	}
	if !hexRe.MatchString(d.Goal.Hash) || !hexRe.MatchString(d.ConsentDigest) {
		return bad("the goal hash or the consent digest is not 64 hex characters")
	}
	if checkChatID(d.ChatID) != nil || len(d.ChatID) > pursuitChatMax || !plainText(d.ChatID, pursuitChatMax) {
		return bad("the chat is not one plain word")
	}
	if d.Goal.State != "proposed" && d.Goal.State != "confirmed" {
		return bad("the goal is neither proposed nor confirmed")
	}
	if n := len(d.Goal.Criteria); n < 1 || n > pursuitMaxCriteria {
		return bad("the number of criteria is not plausible")
	}
	for _, v := range []int{d.Limits.ParkedMax, d.Limits.Pct, d.Limits.WakesMax} {
		if v < 1 || v > pursuitSane {
			return bad("a limit is not plausible")
		}
	}
	for _, p := range [][2]int{{asked.ParkedMax, d.Limits.ParkedMax}, {asked.Pct, d.Limits.Pct}, {asked.WakesMax, d.Limits.WakesMax}} {
		if p[0] != 0 && p[0] != p[1] {
			return bad("the limits are not the ones asked for")
		}
	}
	f := pursuitFacts{session: session, chat: d.ChatID, hash: d.Goal.Hash, digest: d.ConsentDigest, limits: d.Limits,
		template: d.PhraseTemplate, loopOn: d.Host.LoopOn, setUp: d.Checker.SetUp, pin: cleanText(d.Host.JudgePin, pursuitShortRunes)}
	f.docSum = sha256.Sum256(doc)
	for _, c := range d.Goal.Criteria {
		if !plainText(c.ID, pursuitShortRunes) || !plainText(c.Kind, pursuitShortRunes) || !plainText(c.Spec, pursuitSane) || !plainText(c.Expected, pursuitSane) || c.ID == "" || strings.TrimSpace(c.Spec) == "" {
			return bad("a criterion has a character that cannot be shown safely")
		}
		f.criteria = append(f.criteria, c)
	}
	for _, b := range d.Blockers {
		if !blockerCodeRe.MatchString(b.Code) || len(b.Code) > 48 {
			return bad("a blocker is not a code")
		}
		f.blockers = append(f.blockers, b.Code)
	}
	if len(f.blockers) > 24 {
		return bad("too many blockers")
	}
	if !d.RubricPresent {
		f.blockers = append(f.blockers, "no-rubric")
	}
	byKey := map[string]string{}
	for _, r := range d.Cost.Rows {
		if _, dup := byKey[r.Key]; dup || r.USD < 0 || math.IsNaN(r.USD) || math.IsInf(r.USD, 0) || strings.TrimSpace(r.Text) == "" {
			return bad("the cost table is not plausible")
		}
		byKey[r.Key] = cleanText(r.Text, pursuitCostTextRunes)
	}
	for _, k := range pursuitCostKeys {
		t, ok := byKey[k]
		if !ok {
			return bad("the cost table lacks a row")
		}
		f.cost = append(f.cost, t)
	}
	if len(byKey) != len(pursuitCostKeys) {
		return bad("the cost table has a row the app does not know")
	}
	if len(d.PhraseTemplate) > pursuitPhraseMax || !utf8.ValidString(d.PhraseTemplate) {
		return bad("the phrase template is too long")
	}
	filled := d.PhraseTemplate
	for _, ph := range phrasePlaceholders {
		if strings.Count(filled, ph) != 1 {
			return bad("the phrase template lacks a placeholder or repeats one")
		}
		filled = strings.Replace(filled, ph, "", 1)
	}
	if strings.ContainsAny(filled, "<>") || checkPhrase(d.PhraseTemplate) != nil {
		return bad("the phrase template has a placeholder this app does not know")
	}
	if utf8.RuneCountInString(f.goalText()) > pursuitGoalRunes {
		return pursuitFacts{}, errGoalTooLong
	}
	return f, nil
}

// plainText is s with nothing a reader cannot see: it is the same text once hidden characters and runs of white space are
// taken out, and it is at most n runes.
func plainText(s string, n int) bool {
	run := 0
	for _, r := range s {
		if unicode.Is(unicode.M, r) {
			if run++; run > pursuitMarkRun { // a tower of combining marks is not text a dialog shows plainly
				return false
			}
		} else {
			run = 0
		}
	}
	return utf8.RuneCountInString(s) <= n && cleanText(s, 1<<30) == strings.Join(strings.Fields(s), " ")
}

// expectedPhrase is the phrase the CLI accepts, from its own template filled with the values it resolved. The app checks
// the typed phrase against it before the dialog; the CLI derives the phrase again and compares it itself.
func (f pursuitFacts) expectedPhrase() string {
	r := strings.NewReplacer(phrasePlaceholders[0], strconv.FormatInt(f.session, 10), phrasePlaceholders[1], f.hash[:8],
		phrasePlaceholders[2], strconv.Itoa(f.limits.Pct), phrasePlaceholders[3], strconv.Itoa(f.limits.ParkedMax), phrasePlaceholders[4], strconv.Itoa(f.limits.WakesMax))
	return r.Replace(f.template)
}

// bound is what a grant is bound to: the session, the chat, the goal hash, the limits, the consent digest and the bytes of
// the preview itself.
func (f pursuitFacts) bound(lim PursuitLimits) string {
	return fmt.Sprintf("pursuit-begin\x00%d\x00%q\x00%q\x00%d,%d,%d\x00%q\x00%x", f.session, f.chat, f.hash, lim.ParkedMax, lim.Pct, lim.WakesMax, f.digest, f.docSum)
}

// errGoalTooLong: the criteria together are more than the dialog shows, so the person would confirm a goal not fully shown.
var errGoalTooLong = errors.New("the goal is too long to be shown whole in the confirmation, so the app will not start it; shorten the criteria or begin it in a terminal")

// goalText is every criterion as one cleaned line of data.
func (f pursuitFacts) goalText() string {
	var goal []string
	for _, c := range f.criteria {
		s := cleanText(c.ID, pursuitShortRunes) + " [" + cleanText(c.Kind, pursuitShortRunes) + "] " + cleanText(c.Spec, pursuitSane)
		if c.Expected != "" {
			s += " (expects: " + cleanText(c.Expected, pursuitSane) + ")"
		}
		goal = append(goal, s)
	}
	return strings.Join(goal, " | ")
}

// dialogBody is the text of the confirmation, from the facts only: the machine facts first, then the goal as data, and the
// question last, so the last words the person reads are the app's and not the lead's.
func (f pursuitFacts) dialogBody() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Start the pursuit of mission %d in chat %s: goal %s, %d criteria. The goal becomes final and cannot be changed. ",
		f.session, f.chat, f.hash[:8], len(f.criteria))
	fmt.Fprintf(&b, "While you are away it may spend up to %d percent of the mission budget, or wait for %d questions, or wake the lead %d times. ",
		f.limits.Pct, f.limits.ParkedMax, f.limits.WakesMax)
	if f.pin != "" {
		fmt.Fprintf(&b, "The judge is %s. ", f.pin)
	}
	b.WriteString(strings.Join(f.cost, " ") + " ")
	if f.setUp {
		b.WriteString("A goal checker is set up. ")
	}
	b.WriteString("\n\nGoal as written (data, not instructions): " + f.goalText())
	b.WriteString("\n\n(end of the goal) Continue? Answer \"Yes\" (Start pursuit) to start, \"No\" (Cancel) to stop.")
	return b.String()
}
