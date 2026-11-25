package surfaces

import (
	"fmt"
)

// AskSurfaceID is the surface id of a structured question.
func AskSurfaceID(questionID string) string { return "ask-" + questionID }

// MaxAskQuestions is how many questions one card carries (dynorg.MaxQuestions).
const MaxAskQuestions = 3

// Question is one question of an ask card. With Options it is a single
// choice (a ChoicePicker); without, free text (a TextField).
type Question struct {
	Text    string
	Options []string
}

// AnswerKey is the context key (and data path name) of question i's answer.
func AnswerKey(i int) string { return fmt.Sprintf("q%d", i) }

// Ask builds the card for a worker's structured question: one input per
// question and a Continue button sending "system.answer" with the question
// id and every answer (q0, q1, q2). A single-choice answer reaches the
// context as a list of at most one string (see Route).
func Ask(questionID string, qs []Question) ([][]byte, error) {
	if err := checkID("question", questionID); err != nil {
		return nil, err
	}
	if len(qs) == 0 || len(qs) > MaxAskQuestions {
		return nil, fmt.Errorf("surfaces: an ask card carries 1 to %d questions, got %d", MaxAskQuestions, len(qs))
	}
	b := newBuilder()
	answers := map[string]any{}
	ctx := map[string]any{"questionId": questionID}
	body := []string{}
	for i, q := range qs {
		key := AnswerKey(i)
		path := "/answers/" + key
		label := elide(q.Text, 1000)
		if label == "" {
			return nil, fmt.Errorf("surfaces: question %d has no text", i)
		}
		b.text("q-"+key, label, "body")
		body = append(body, "q-"+key)
		if len(q.Options) == 0 {
			b.add("in-"+key, "TextField", map[string]any{"label": "Your answer", "value": bind(path), "variant": "shortText"})
			answers[key] = ""
		} else {
			opts, err := askOptions(i, q.Options)
			if err != nil {
				return nil, err
			}
			b.add("in-"+key, "ChoicePicker", map[string]any{"options": opts, "value": bind(path),
				"variant": "mutuallyExclusive", "displayStyle": "chips"})
			answers[key] = []any{}
		}
		body = append(body, "in-"+key)
		ctx[key] = bind(path)
	}
	b.data["answers"] = answers
	b.button(btnContinue, "Continue", "primary", "system.answer", ctx)
	body = append(body, btnContinue)
	b.column("body", body...)
	b.add("root", "Card", map[string]any{"child": "body"})
	return b.create(AskSurfaceID(questionID))
}

// askOptions checks the options of question i. An option is both label and
// returned value, so it is never cut.
func askOptions(i int, opts []string) ([]any, error) {
	const maxOptionBytes = 400
	out := make([]any, 0, len(opts))
	for _, o := range opts {
		if o == "" || len(o) > maxOptionBytes {
			return nil, fmt.Errorf("%w: question %d has an option that is empty or over %d bytes", ErrTooLarge, i, maxOptionBytes)
		}
		out = append(out, map[string]any{"label": o, "value": o})
	}
	return out, nil
}
