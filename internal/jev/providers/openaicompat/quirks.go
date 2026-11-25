package openaicompat

import (
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

// forgetQuirks drops everything learned (tests: a new fake server may get an
// earlier one's port).
func forgetQuirks() {
	quirksMu.Lock()
	defer quirksMu.Unlock()
	clear(quirksOf)
}
