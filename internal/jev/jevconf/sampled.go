package jevconf

// Sampled thresholds. A sampling provider's probabilities are add-one
// smoothed shares of a few answers, so five agreeing answers read
// (n+1)/(n+k): 6/7 on two options, 0.75 on three, less with more. On a
// LOW-RISK surface a threshold above that is lowered to it, so only a
// unanimous result passes; everywhere else a sampled answer is compared with
// the configured threshold (owner decision, 2026-11-26).

import (
	"math"

	"github.com/monoes/mono-agent/internal/jev"
)

// sampledLowered is the ONE list of surfaces that lowering applies to. A
// surface not named here, every new one too, compares with the configured
// threshold. To be here a surface may only label, suggest, delay or stop, or
// pick among things that need no approval: nothing it decides may send,
// approve, click, type or resume a run. sampled_test.go pins the list.
var sampledLowered = map[Surface]bool{
	Capture:     true, // a suggested route; nothing is routed by itself
	Inbox:       true, // an intent label on a message
	PeopleLinks: true, // a suggested link; a dismissal keeps its calibrated-only rule
	Retry:       true, // retry triage: it only delays or stops a retry
	APIAuto:     true, // the auto model's pick among the models the server serves
}

// sampledSlack absorbs the rounding between jev.UnanimousProbability and what
// an answer built from a unanimous sample reads. It is far below the
// difference one sample makes.
const sampledSlack = 1e-9

// LowersSampled reports whether s is on the list.
func LowersSampled(s Surface) bool { return sampledLowered[s] }

// SampledLoweredSurfaces is the listed surfaces, in Surfaces order.
func SampledLoweredSurfaces() []Surface {
	var out []Surface
	for _, s := range Surfaces {
		if sampledLowered[s] {
			out = append(out, s)
		}
	}
	return out
}

// SampledCeiling is what surface s may lower its threshold to for answer a:
// the probability a unanimous result reads, when s is on the list, resp is
// sampled and the adapter recorded it (jev.Answer.Unanimous; an answer with
// any request read from log-probabilities has none). Else 0, no lowering.
func SampledCeiling(s Surface, resp *jev.Response, a jev.Answer) float64 {
	if !sampledLowered[s] || resp == nil || resp.Probability != jev.ProbSampled {
		return 0
	}
	return a.Unanimous
}

// CapThreshold is the threshold in force for an answer with a SampledCeiling:
// configured, or the ceiling when configured is above it. A ceiling of 0
// leaves configured as it is.
func CapThreshold(configured, ceiling float64) float64 {
	if ceiling <= 0 {
		return configured
	}
	return math.Min(configured, ceiling-sampledSlack)
}

// GateThreshold is what surface s compares a's gate value with: configured,
// except on a listed surface for a sampled answer, where a unanimous result
// passes any threshold and anything less passes only what it reaches on its
// own. A rule that needs calibrated probabilities checks resp.Probability
// itself.
func GateThreshold(s Surface, resp *jev.Response, a jev.Answer, configured float64) float64 {
	return CapThreshold(configured, SampledCeiling(s, resp, a))
}
