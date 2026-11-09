package verifier

import "strings"

// PinKey is a pin without its version, trimmed and in lower case: "runtime/model@1", "Runtime/Model" and "runtime/model" are
// the same name for the independence rule. It is the rule of the pursuit judge's check in the coder host (pinKey there),
// kept equal by one table of vectors that both packages' tests hold (pin_test.go reads the coderhost copy). It compares NAMES: two names for one model (a provider alias) are not recognised, so the rule is not
// a guarantee of different weights.
func PinKey(p string) string {
	k, _, _ := strings.Cut(strings.TrimSpace(p), "@")
	return strings.ToLower(strings.TrimSpace(k))
}

// Independent is the pairwise rule: the judge, the checker and the lead must each be known and all different. why is a plain
// sentence when they are not. The lead's pin is read by the host at call time, so a lead whose model is not known (empty)
// is not shown to differ and refuses.
func Independent(judge, checker, lead string) (ok bool, why string) {
	j, c, l := PinKey(judge), PinKey(checker), PinKey(lead)
	switch {
	case c == "":
		return false, "the checker's model is not set"
	case j == "":
		return false, "the judge's model is not known, so the checker cannot be shown to differ from it"
	case l == "":
		return false, "the lead's model is not known, so the checker cannot be shown to differ from it"
	case c == j:
		return false, "the checker's model is the judge's"
	case c == l:
		return false, "the checker's model is the lead's"
	case j == l:
		return false, "the judge's model is the lead's"
	}
	return true, ""
}
