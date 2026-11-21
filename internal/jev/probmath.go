package jev

import (
	"math"
	"strings"
)

// The probability arithmetic every provider shares (plan D6), with Jev's
// published formulas, so a confidence means the same thing whoever answered.

// ChoiceConfidence is how far the most probable option stands above a
// uniform guess: (p_max − 1/n) / (1 − 1/n), in [0,1]. One option is certain
// (1); no options is 0.
func ChoiceConfidence(probs map[string]float64) float64 {
	n := len(probs)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return 1
	}
	top := 0.0
	for _, p := range probs {
		top = math.Max(top, p)
	}
	u := 1 / float64(n)
	return clamp01((top - u) / (1 - u))
}

// ScoreConfidence is how concentrated a distribution over ordered levels is
// around its most likely level m: max(0, 1 − Σ pᵢ·|i−m| / MAD_unif), where
// MAD_unif is the same sum under a uniform distribution. Ties for m go to the
// lowest level. One level is certain (1); none is 0.
func ScoreConfidence(probs []float64) float64 {
	n := len(probs)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return 1
	}
	m := mostLikely(probs)
	mad, unif := 0.0, 0.0
	for i, p := range probs {
		d := math.Abs(float64(i - m))
		mad += p * d
		unif += d / float64(n)
	}
	return clamp01(1 - mad/unif)
}

// ExpectedScore is the expected level Σ i·pᵢ (normalised by Σ pᵢ; 0 when the
// distribution has no mass).
func ExpectedScore(probs []float64) float64 {
	sum, e := 0.0, 0.0
	for i, p := range probs {
		sum += p
		e += float64(i) * p
	}
	if sum <= 0 {
		return 0
	}
	return e / sum
}

// Renormalise scales probs to sum to 1. It returns nil when a value is
// negative, NaN or infinite, or when there is no mass at all.
func Renormalise(probs map[string]float64) map[string]float64 {
	sum := 0.0
	for _, p := range probs {
		if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 {
			return nil
		}
		sum += p
	}
	if sum <= 0 {
		return nil
	}
	out := make(map[string]float64, len(probs))
	for id, p := range probs {
		out[id] = p / sum
	}
	return out
}

// TokenLogprob is one alternative of a generated token, in the shape OpenAI's
// top_logprobs entries have.
type TokenLogprob struct {
	Token   string  `json:"token"`
	Logprob float64 `json:"logprob"`
}

// FromLogprobs reads a distribution over labels from a token's top
// alternatives. A token counts for a label when it equals the label after
// leading whitespace is dropped, compared exactly first and then ignoring
// case (so "A", " A" and "a" all count for "A"); the variants' masses are
// summed. probs holds every label (0 for those with no mass) renormalised
// over the labels; coverage is the raw probability mass that fell on any
// label, in [0,1]. With no mass on any label probs is nil and coverage 0.
func FromLogprobs(top []TokenLogprob, labels []string) (probs map[string]float64, coverage float64) {
	mass := make(map[string]float64, len(labels))
	for _, l := range labels {
		mass[l] = 0
	}
	for _, t := range top {
		if math.IsNaN(t.Logprob) {
			continue
		}
		l, ok := matchLabel(t.Token, labels)
		if !ok {
			continue
		}
		p := math.Exp(t.Logprob)
		mass[l] += p
		coverage += p
	}
	if coverage <= 0 {
		return nil, 0
	}
	return Renormalise(mass), math.Min(coverage, 1)
}

func matchLabel(token string, labels []string) (string, bool) {
	tok := strings.TrimLeft(token, " \t\r\n")
	if tok == "" {
		return "", false
	}
	for _, l := range labels {
		if tok == l {
			return l, true
		}
	}
	for _, l := range labels {
		if strings.EqualFold(tok, l) {
			return l, true
		}
	}
	return "", false
}

// mostLikely is the index of the largest value, the lowest on a tie.
func mostLikely(probs []float64) int {
	m := 0
	for i, p := range probs {
		if p > probs[m] {
			m = i
		}
	}
	return m
}

func clamp01(f float64) float64 { return math.Max(0, math.Min(1, f)) }
