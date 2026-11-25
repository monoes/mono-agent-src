package openaicompat

import (
	"regexp"
	"strings"
	"sync"
	"time"
)

// What a server refused for a model is shared by every backend of this
// process, so a new client (every surface builds its own) does not collect
// the same 400s again. Nothing is stored on disk, and an entry is dropped
// quirksTTL after it was first learned, so a server that changed behind the
// same URL and model name is asked afresh.

const quirksTTL = time.Hour

// quirks is what one server refused for one model.
type quirks struct {
	// completionTokens and noTemperature: max_tokens or temperature was
	// refused (complete).
	completionTokens, noTemperature bool
	// largeBudget: completionBudget was spent reasoning with nothing
	// answered, so largeCompletionBudget is asked.
	largeBudget bool
	// topLogprobs is the most alternatives accepted; 0 = no cap was found.
	topLogprobs int
}

type quirksKey struct{ base, model string }

type learnedQuirks struct {
	quirks
	since time.Time
}

var (
	quirksMu  sync.Mutex
	quirksOf  = map[quirksKey]learnedQuirks{}
	quirksNow = time.Now // replaced by tests
)

// recall is what is known about model at base.
func recall(base, model string) quirks {
	quirksMu.Lock()
	defer quirksMu.Unlock()
	return current(quirksKey{base, model}).quirks
}

// remember records what change adds about model at base.
func remember(base, model string, change func(*quirks)) {
	quirksMu.Lock()
	defer quirksMu.Unlock()
	k := quirksKey{base, model}
	l := current(k)
	if l.since.IsZero() {
		l.since = quirksNow()
	}
	change(&l.quirks)
	if l.quirks == (quirks{}) {
		delete(quirksOf, k)
		return
	}
	quirksOf[k] = l
}

// current is k's entry, dropping it when it is too old. The caller holds quirksMu.
func current(k quirksKey) learnedQuirks {
	l, ok := quirksOf[k]
	if ok && quirksNow().Sub(l.since) >= quirksTTL {
		delete(quirksOf, k)
		return learnedQuirks{}
	}
	return l
}

// trial is what the requests of one question, or of one probe, found refused
// and go without. A refusal becomes the process's memory only once a request
// without that parameter succeeds.
type trial struct {
	// probe asks with every parameter again, whatever is remembered; what its
	// successful request did not need is forgotten.
	probe bool

	mu sync.Mutex // a question's samples run at once
	q  quirks     // completionTokens, noTemperature and largeBudget only
}

// adapt shapes body for what was refused so far: remembered (not for a probe)
// or found by this trial.
func (tr *trial) adapt(base string, body chatRequest) chatRequest {
	var q quirks
	if !tr.probe {
		q = recall(base, body.Model)
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if (q.completionTokens || tr.q.completionTokens) && body.MaxTokens > 0 {
		body.MaxTokens, body.MaxCompletionTokens = 0, completionBudget
		if q.largeBudget || tr.q.largeBudget {
			body.MaxCompletionTokens = largeCompletionBudget
		}
	}
	if q.noTemperature || tr.q.noTemperature {
		body.Temperature = nil
	}
	return body
}

// refused reads the body of a 400 to sent: one that names max_tokens or
// temperature as unsupported, and not logprobs, switches that parameter off for
// this trial and reports true when sent still carried it, so each request in
// flight at the refusal is sent again and none for the same parameter twice.
// Anything else is left to the caller.
func (tr *trial) refused(sent chatRequest, body string) bool {
	if strings.Contains(strings.ToLower(body), "logprobs") {
		return false
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	switch {
	case sent.MaxTokens > 0 && namesUnsupported(body, "max_tokens"):
		tr.q.completionTokens = true
	case sent.Temperature != nil && namesUnsupported(body, "temperature"):
		tr.q.noTemperature = true
	default:
		return false
	}
	return true
}

// succeeded records what the success of sent shows about base.
func (tr *trial) succeeded(base string, sent chatRequest) {
	tr.mu.Lock()
	found := tr.q
	tr.mu.Unlock()
	without := quirks{completionTokens: sent.MaxTokens == 0, noTemperature: sent.Temperature == nil}
	if !tr.probe && !(found.completionTokens && without.completionTokens) && !(found.noTemperature && without.noTemperature) {
		return
	}
	remember(base, sent.Model, func(q *quirks) {
		if tr.probe {
			q.completionTokens, q.noTemperature = without.completionTokens, without.noTemperature
			q.largeBudget = q.largeBudget && without.completionTokens
			if sent.TopLogprobs == defaultTopK {
				q.topLogprobs = 0
			}
			return
		}
		q.completionTokens = q.completionTokens || found.completionTokens && without.completionTokens
		q.noTemperature = q.noTemperature || found.noTemperature && without.noTemperature
	})
}

// namesTopLogprobs says whether the body of a 400 is about the number of
// alternatives asked for: only then is a cap on it remembered.
func namesTopLogprobs(body string) bool {
	return strings.Contains(strings.ToLower(body), "top_logprobs")
}

// unsupportedCode is OpenAI's error code for a parameter or value a model takes
// none of.
var unsupportedCode = regexp.MustCompile(`"code"\s*:\s*"(unsupported_parameter|unsupported_value|unknown_parameter)"`)

// namesUnsupported says whether the body of a 400 refuses the parameter param
// itself, as unsupported: by OpenAI's param and code, or in words that sit
// right beside its name. A body that only mentions it (a context length, a
// value out of range) does not.
func namesUnsupported(body, param string) bool {
	s := strings.ToLower(body)
	if unsupportedCode.MatchString(s) && regexp.MustCompile(`"param"\s*:\s*"`+param+`"`).MatchString(s) {
		return true
	}
	name := `\\?['"`+"`"+`]?\b` + param + `\b\\?['"`+"`"+`]?`
	before := `(unsupported (parameter|value|argument)|unrecognized( request)? arguments?( supplied)?|unknown (parameter|argument)|(does not|doesn't) support( the)?( parameter)?)\s*:?\s*`
	after := `(\s+parameter)?\s+((is\s+)?(not supported|unsupported|not allowed|not permitted|deprecated)|(does not|doesn't) support)`
	return regexp.MustCompile(before+name).MatchString(s) || regexp.MustCompile(name+after).MatchString(s)
}

// forgetQuirks drops everything learned (tests: a new fake server may get an
// earlier one's port).
func forgetQuirks() {
	quirksMu.Lock()
	defer quirksMu.Unlock()
	clear(quirksOf)
}
