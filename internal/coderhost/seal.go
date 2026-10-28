package coderhost

// What a model wrote into a parked question or a ledger row is returned to another session (a
// master reading its subtree) only as cleaned one-line data: no brackets, angle brackets, quotes,
// control or invisible characters, so it cannot imitate a fence or a header line there.

// SealQuestion returns q with the text a model wrote cleaned.
func SealQuestion(q Question) Question {
	q.Question = cleanText(q.Question, maxQuestionText)
	q.Default = cleanText(q.Default, maxQuestionText)
	q.Answer = cleanText(q.Answer, MaxAnswerBytes)
	opts := make([]string, len(q.Options))
	for i, o := range q.Options {
		opts[i] = cleanText(o, maxOptionText)
	}
	q.Options = opts
	return q
}

// SealEntry returns e with the text a model wrote cleaned.
func SealEntry(e LedgerEntry) LedgerEntry {
	e.Question = cleanText(e.Question, maxDecisionText)
	e.Choice = cleanText(e.Choice, maxDecisionText)
	e.Reason = cleanText(e.Reason, maxDecisionText)
	alts := make([]string, len(e.Alternatives))
	for i, a := range e.Alternatives {
		alts[i] = cleanText(a, maxAlternativeText)
	}
	e.Alternatives = alts
	return e
}
