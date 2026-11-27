package jev

import "strings"

// GateValue is Top for a surface that gates on the answer: the most probable
// option and the probability compared with the threshold. An answer whose
// provider gives no probability (ProbNone) gates as 0, so it never passes a
// threshold > 0 and the surface falls back to what it does without Jev (plan
// D7). Only a "high p ⇒ act" gate may use p as is: 0 here means "unknown",
// not "confident no", so a site that acts on a LOW p (dismiss, reject) must
// check resp.Probability == ProbNone itself and do nothing.
func GateValue(resp *Response, a Answer) (id string, p float64) {
	id, p = Top(a)
	return id, GateProbability(resp, p)
}

// GateProbability is the one rule behind GateValue, for a surface that gates
// on a probability other than the top option's (a noul, one option's): p,
// or 0 when resp's provider gives no probability. Every other kind
// (native, logprob, softmax, sampled) is a real number and gates as is.
func GateProbability(resp *Response, p float64) float64 {
	if resp != nil && resp.Probability == ProbNone {
		return 0
	}
	return p
}

// UnanimousProbability is the probability SampledProbs gives the option that
// every one of samples answers named, on a question of options options:
// (samples+1)/(samples+options). 0 when either is not positive. A sampling
// adapter records it as Answer.Unanimous.
func UnanimousProbability(samples, options int) float64 {
	if samples <= 0 || options <= 0 {
		return 0
	}
	return float64(samples+1) / float64(samples+options)
}

// ModelLabel is the label a surface stores for the model that answered: the
// model alone for TypeSafe ("" counts as TypeSafe), so rows stay as they were
// before decision providers, else "<provider>/<model>" with any run of
// white space in the model replaced by "-", or the provider alone when the
// model is unknown (never "<provider>/").
func ModelLabel(provider, model string) string {
	if provider == "" || provider == ProviderTypeSafe {
		return model
	}
	model = strings.Join(strings.Fields(model), "-")
	if model == "" {
		return provider
	}
	return provider + "/" + model
}

// ResponseLabel is ModelLabel of resp, with fallbackModel (the client's
// model) when resp names none.
func ResponseLabel(resp *Response, fallbackModel string) string {
	if resp == nil {
		return fallbackModel
	}
	model := resp.Model
	if model == "" {
		model = fallbackModel
	}
	return ModelLabel(resp.Provider, model)
}
