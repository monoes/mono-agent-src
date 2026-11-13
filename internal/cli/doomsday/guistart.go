package doomsday

import (
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// guiAck is the acknowledgement of a start the desktop app runs for a person who typed the phrase in its form. The
// CLI has no terminal then, and it does not trust the caller for the phrase: it derives it again from the charter it
// just rendered (the number of lifted lines and the first 8 characters of the hash) and compares. The kind "gui" is
// the audit row's record that the phrase came through the app, not from a terminal or a script's full hash. It is the
// same trust class as a script's flag: the operator gate (the caller is the user's own process, not an agent, a
// workflow or a coder turn) is what keeps others out, and the CLI cannot tell the app from a script that passes this
// hidden flag.
func guiAck(lines int, full string, yes bool, hash, typed string) (Ack, error) {
	if yes || hash != "" {
		return Ack{}, clicore.InvalidInput("--gui-phrase does not go with --yes-i-lift or --hash: give one acknowledgement")
	}
	if strings.TrimSpace(typed) != Phrase(lines, full) {
		return Ack{}, clicore.InvalidInput("the phrase does not match; nothing started")
	}
	return Ack{Kind: "gui", Lines: lines}, nil
}

// PhraseTemplate is the shape of the phrase the form asks for, with the two numbers the person reads off the preview
// (the count of lifted lines and the first 8 characters of the hash). It is never the phrase itself.
const PhraseTemplate = "I understand: lift <N> lines, charter <first 8 characters of the hash>"

// RenderDoc is `render --json`: the app's preview. It carries what the person must read (the full hash, every lifted
// line by name, the whole rendered text with the plain limits of the mode) and the template of the phrase, never the
// phrase: the form asks the person to write it from the count and the hash it shows.
type RenderDoc struct {
	Hash           string   `json:"hash"`
	Lines          []string `json:"lines"`
	LiftCount      int      `json:"lift_count"`
	Text           string   `json:"text"`
	PhraseTemplate string   `json:"phrase_template"`
	// Summary is what the app's native dialog is built from, so that a swapped charter does not look the same there.
	Summary RenderSummary `json:"summary"`
	// UnboundWarning is "running without peer-credential binding: <reason>" when a run started now would go unbound; the app's native
	// confirmation shows it. Empty: nothing to say.
	UnboundWarning string `json:"unbound_warning,omitempty"`
}

// RenderSummary is the CLI's own short account of a charter: the start of the goal, how many criteria, the concurrency
// limits and the pinned models. Every text in it is escaped and cut as the render's is.
type RenderSummary struct {
	Goal          string `json:"goal"`
	Criteria      int    `json:"criteria"`
	TreeDepth     int    `json:"tree_depth"`
	LivePerTree   int    `json:"live_per_tree"`
	MaxConcurrent int    `json:"max_concurrent"`
	MaxTotal      int    `json:"max_total"`
	BossModel     string `json:"boss_model"`
	VerifierModel string `json:"verifier_model"`
	// AutoApprove says what the boss may approve by itself ("nothing" when no per-ask limit is declared), and
	// AnswerClasses the critical asks it may answer by itself; both escaped.
	AutoApprove   string   `json:"auto_approve"`
	AnswerClasses []string `json:"answer_classes"`
}

func autoApproveText(p PerAsk) string {
	if !p.AutoApproves() {
		return "nothing"
	}
	return fmt.Sprintf("spend up to %.6f USD and %d tasks per ask, access %s, %d levels down; run caps %.6f USD and %d tasks",
		float64(p.SpendMicro)/1e6, p.Tasks, plainN(p.Access, 40), p.Depth, float64(p.RunSpendMicro)/1e6, p.RunTasks)
}

func classesOf(p PerAsk) []string {
	out := make([]string, 0, len(p.AnswerClasses))
	for _, c := range p.AnswerClasses {
		out = append(out, plainN(c, 60))
	}
	return out
}

func pinText(p Pin) string {
	return plainN(p.Provider, 60) + "/" + plainN(p.Model, 60) + " " + plainN(p.Version, 30)
}

// NewRenderDoc is the preview of a charter whose canonical bytes carry hash.
func NewRenderDoc(c Charter, hash string) RenderDoc {
	lines := make([]string, 0, len(c.Lines))
	for _, l := range c.Lines {
		lines = append(lines, string(l))
	}
	return RenderDoc{Hash: hash, Lines: lines, LiftCount: len(lines), Text: Render(c, hash), PhraseTemplate: PhraseTemplate,
		Summary: RenderSummary{Goal: plainN(c.Goal, 300), Criteria: len(c.Criteria), TreeDepth: c.Limits.TreeDepth, LivePerTree: c.Limits.LivePerTree,
			MaxConcurrent: c.Limits.MaxConcurrent, MaxTotal: c.Limits.MaxTotal, BossModel: pinText(c.Pins.Boss), VerifierModel: pinText(c.Pins.Verifier),
			AutoApprove: autoApproveText(c.PerAsk), AnswerClasses: classesOf(c.PerAsk)}}
}

// ValidateGUIStart holds a start from the app to what its form requires, whatever the page sent: a success criterion
// (a run with none never finishes on its own), and max concurrent not above live per tree even where the charter chose
// that on purpose. A budget choice is Validate's own rule (neither or both is refused). The terminal keeps its latitude.
func ValidateGUIStart(c Charter) error {
	switch {
	case len(c.Criteria) == 0:
		return clicore.InvalidInput("a start from the app needs at least one success criterion")
	case c.Limits.MaxConcurrent > c.Limits.LivePerTree:
		return clicore.InvalidInput("a start from the app needs max_concurrent not above live_per_tree")
	}
	// Every text the person is shown is shown whole and as it is: the hash and the boss see all of it, so a text longer
	// than the display cap, or one with a character a person cannot see, is refused rather than shown cut or escaped.
	texts := charterStrings(reflect.ValueOf(c)) // every string the charter carries, found by its type, so a new field is checked too
	for _, t := range texts {
		if utf8.RuneCountInString(t) > MaxDisplayRunes {
			return clicore.InvalidInput("a start from the app takes text of at most %d characters in the goal, criteria and pins", MaxDisplayRunes)
		}
		for _, r := range t {
			if r != '\n' && r != '\t' && hiddenRune(r) {
				return clicore.InvalidInput("a start from the app takes no hidden characters (zero-width, direction, tag or control characters) in the goal, criteria and pins: U+%04X", r)
			}
		}
	}
	return nil
}

// MaxDisplayRunes is the cap of every free text the render shows whole.
const MaxDisplayRunes = 2000

// charterStrings is every string inside v, whatever the field is called or how it is held: strings, structs, slices,
// arrays, pointers, interfaces and maps (keys and values).
func charterStrings(v reflect.Value) []string {
	switch v.Kind() {
	case reflect.String:
		return []string{v.String()}
	case reflect.Struct:
		var out []string
		for i := 0; i < v.NumField(); i++ {
			out = append(out, charterStrings(v.Field(i))...)
		}
		return out
	case reflect.Slice, reflect.Array:
		var out []string
		for i := 0; i < v.Len(); i++ {
			out = append(out, charterStrings(v.Index(i))...)
		}
		return out
	case reflect.Ptr, reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return charterStrings(v.Elem())
	case reflect.Map:
		var out []string
		for _, k := range v.MapKeys() {
			out = append(out, charterStrings(k)...)
			out = append(out, charterStrings(v.MapIndex(k))...)
		}
		return out
	}
	return nil
}
