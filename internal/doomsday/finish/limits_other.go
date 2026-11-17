//go:build windows

package finish

// No criterion is run on Windows (the verifier has no process containment there, so a goal check answers cannot-check), so there is no helper.
func limitsArgv(Limits, string, []string) (string, []string, bool) { return "", nil, false }

// RunLimitsHelperIfRequested does nothing: there is no helper on Windows.
func RunLimitsHelperIfRequested([]string) {}
