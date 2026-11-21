package jev

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"sync"
)

// Helpers a Backend uses to turn a distribution into an Answer that passes
// Validate, and to answer a request one question at a time.

// OrderedOptionIDs is OptionIDs in a stable order: a choice's ids sorted, a
// score's levels "0".."n-1" in level order. A noul question has none.
func OrderedOptionIDs(q Question) []string {
	if q.Type == TypeNoul {
		return nil
	}
	ids := OptionIDs(q)
	if q.Type == TypeScore {
		sort.Slice(ids, func(i, j int) bool { a, _ := strconv.Atoi(ids[i]); b, _ := strconv.Atoi(ids[j]); return a < b })
	} else {
		sort.Strings(ids)
	}
	return ids
}

// AnswerFrom builds q's answer from a distribution: over q's option ids for
// a choice, over levels "0".."n-1" for a score, over "true"/"false" for a
// noul. Options probs leaves out get 0; the mass is renormalised. An id q
// does not offer, a negative or non-finite value, or no mass at all is an
// error wrapping ErrInvalidAnswer, never a guess.
func AnswerFrom(q Question, probs map[string]float64) (Answer, error) {
	switch q.Type {
	case TypeChoice:
		return ChoiceAnswer(q, probs)
	case TypeScore:
		return ScoreAnswer(q, probs)
	case TypeNoul:
		p, err := over([]string{"true", "false"}, probs)
		if err != nil {
			return Answer{}, err
		}
		return NoulAnswer(p["true"])
	}
	return Answer{}, fmt.Errorf("%w: unknown question type %q", ErrInvalidAnswer, q.Type)
}

// ChoiceAnswer is AnswerFrom for a choice: the most probable option (the
// lowest id on a tie), the renormalised probabilities and ChoiceConfidence.
func ChoiceAnswer(q Question, probs map[string]float64) (Answer, error) {
	ids := OrderedOptionIDs(q)
	p, err := over(ids, probs)
	if err != nil {
		return Answer{}, err
	}
	best := ids[0]
	for _, id := range ids {
		if p[id] > p[best] {
			best = id
		}
	}
	return Answer{Type: TypeChoice, Choice: best, Probabilities: p, Confidence: ChoiceConfidence(p)}, nil
}

// ScoreAnswer is AnswerFrom for a score: the expected level as Score, the
// renormalised probabilities keyed "0".."n-1", the levels' texts as Legend
// and ScoreConfidence.
func ScoreAnswer(q Question, probs map[string]float64) (Answer, error) {
	ids := OrderedOptionIDs(q)
	p, err := over(ids, probs)
	if err != nil {
		return Answer{}, err
	}
	levels := make([]float64, len(ids))
	for i, id := range ids {
		levels[i] = p[id]
	}
	a := Answer{Type: TypeScore, Probabilities: p, Confidence: ScoreConfidence(levels)}
	// Clamped: rounding must not put the expectation outside 0..n-1.
	a.Score = math.Max(0, math.Min(float64(len(ids)-1), ExpectedScore(levels)))
	if legend := scoreLegend(q); len(legend) > 0 {
		a.Legend = legend
	}
	return a, nil
}

// NoulAnswer is a noul answer of P(yes) = pYes, which must be in [0,1].
func NoulAnswer(pYes float64) (Answer, error) {
	if !unit(pYes) {
		return Answer{}, fmt.Errorf("%w: noul %v outside [0,1]", ErrInvalidAnswer, pYes)
	}
	return Answer{Type: TypeNoul, Noul: pYes}, nil
}

// over renormalises probs over exactly ids (missing ids get 0).
func over(ids []string, probs map[string]float64) (map[string]float64, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("%w: the question has no options", ErrInvalidAnswer)
	}
	full := make(map[string]float64, len(ids))
	for _, id := range ids {
		full[id] = 0
	}
	for id, p := range probs {
		if _, ok := full[id]; !ok {
			return nil, fmt.Errorf("%w: %q was not offered", ErrInvalidAnswer, id)
		}
		full[id] = p
	}
	out := Renormalise(full)
	if out == nil {
		return nil, fmt.Errorf("%w: no usable probability mass", ErrInvalidAnswer)
	}
	return out, nil
}

func scoreLegend(q Question) map[string]string {
	out := map[string]string{}
	switch c := q.Criteria.(type) {
	case []string:
		for i, s := range c {
			out[strconv.Itoa(i)] = s
		}
	case []any:
		for i, s := range c {
			out[strconv.Itoa(i)] = fmt.Sprint(s)
		}
	}
	return out
}

// DefaultMaxInFlight bounds FanOut when its caller passes no limit (plan D8).
const DefaultMaxInFlight = 8

// FanOut answers req one question at a time, for a backend that takes one
// question per request: at most maxInFlight calls of one run at once (≤ 0 =
// DefaultMaxInFlight). The usage of every call that returned is summed
// (CostUSD is set when any call reported one). On the first error the other
// calls are cancelled and FanOut returns that error, wrapped with the
// question id, together with the response so far, so the usage of the calls
// already billed is still recorded.
func FanOut(ctx context.Context, req *Request, maxInFlight int,
	one func(ctx context.Context, id string, q Question) (Answer, Usage, error)) (*Response, error) {
	if maxInFlight <= 0 {
		maxInFlight = DefaultMaxInFlight
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	resp := &Response{Model: req.Model, Answers: make(map[string]Answer, len(req.Questions))}
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		firstErr error
	)
	slots := make(chan struct{}, maxInFlight)
	ids := make([]string, 0, len(req.Questions))
	for id := range req.Questions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(id string, q Question) {
			defer wg.Done()
			defer func() { <-slots }()
			a, u, err := one(ctx, id, q)
			mu.Lock()
			defer mu.Unlock()
			addUsage(&resp.Usage, u)
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("%s: %w", id, err)
					cancel()
				}
				return
			}
			resp.Answers[id] = a
		}(id, req.Questions[id])
	}
	wg.Wait()
	if firstErr == nil && len(resp.Answers) < len(req.Questions) {
		firstErr = ctx.Err() // the loop stopped early: the caller's context ended
	}
	return resp, firstErr
}

func addUsage(sum *Usage, u Usage) {
	sum.InputTokens += u.InputTokens
	sum.OutputTokens += u.OutputTokens
	if u.CostUSD != nil {
		c := *u.CostUSD
		if sum.CostUSD != nil {
			c += *sum.CostUSD
		}
		sum.CostUSD = &c
	}
}
