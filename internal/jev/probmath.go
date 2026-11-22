package jev

import (
	"math"
	"sort"
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

// tieGap is how far Conservative keeps every other option below the most
// probable one when the spread would otherwise tie them, so the answer stays
// the option the model gave the most mass.
const tieGap = 1e-12

// Conservative turns the raw mass a model put on each option (summing to at
// most 1; the rest went to answers that are no option) into a distribution
// over the options that sums to 1 without overstating the answer: a
// provider may understate its confidence, never overstate it. The most
// probable option (the first key in sort order on a tie) keeps its raw
// mass, or 1/n when that is less (no distribution of n options has a lower
// top). The rest, 1 − top, goes to the other options in proportion to their
// own mass (evenly when they have none), none of them above the top: an
// option that would rise above it is held just below it and its excess
// spread over the others. So the reported top never exceeds
// max(raw top mass, 1/n) + 1e-12, and the answer never moves to an option
// the model judged less likely. A mass that already sums to 1 (or more) is
// simply renormalised. nil when a value is negative, NaN or infinite, or
// when there is no mass at all.
func Conservative(mass map[string]float64) map[string]float64 {
	sum := 0.0
	keys := make([]string, 0, len(mass))
	for id, p := range mass {
		if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 {
			return nil
		}
		sum += p
		keys = append(keys, id)
	}
	if sum <= 0 {
		return nil
	}
	if sum >= 1 || len(keys) == 1 {
		return Renormalise(mass)
	}
	sort.Strings(keys)
	top := keys[0]
	for _, id := range keys {
		if mass[id] > mass[top] {
			top = id
		}
	}
	n := len(keys)
	pTop := math.Max(mass[top], 1/float64(n))
	out := make(map[string]float64, n)
	out[top] = pTop
	free := make([]string, 0, n-1)
	for _, id := range keys {
		if id != top {
			free = append(free, id)
		}
	}
	rest := 1 - pTop
	for len(free) > 0 {
		w := 0.0
		for _, id := range free {
			w += mass[id]
		}
		share := func(id string) float64 {
			if w <= 0 {
				return rest / float64(len(free))
			}
			return rest * mass[id] / w
		}
		var keep []string
		for _, id := range free {
			if share(id) >= pTop {
				out[id] = pTop
			} else {
				keep = append(keep, id)
			}
		}
		if len(keep) == len(free) {
			for _, id := range free {
				out[id] = share(id)
			}
			break
		}
		rest -= pTop * float64(len(free)-len(keep))
		free = keep
	}
	for _, id := range keys {
		if id != top && out[id] > out[top]-tieGap {
			out[id] -= tieGap
			out[top] += tieGap
		}
	}
	return out
}

// SampledProbs is the distribution k samples give over options: add-one
// (Laplace) smoothing over the samples asked,
//
//	p(o) = (count(o) + 1) / (asked + n_options),
//
// where a sample that named no option (junk) counts toward asked but toward
// no option, then Conservative for the mass junk left over. So 5 of 5 on a
// two-option question is 6/7 ≈ 0.857 and on three options 6/8 = 0.75: five
// samples never clear a 0.9 threshold. counts of an id not in options are
// ignored.
func SampledProbs(counts map[string]int, asked int, options []string) map[string]float64 {
	return sampleShares(counts, asked, options, 1)
}

// sampleShares is SampledProbs with prior pseudo-counts per option (0 = the
// plain shares of the samples asked).
func sampleShares(counts map[string]int, asked int, options []string, prior float64) map[string]float64 {
	if len(options) == 0 {
		return nil
	}
	denom := float64(asked) + prior*float64(len(options))
	if denom <= 0 {
		return nil
	}
	mass := make(map[string]float64, len(options))
	for _, o := range options {
		mass[o] = (float64(counts[o]) + prior) / denom
	}
	return Conservative(mass)
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
