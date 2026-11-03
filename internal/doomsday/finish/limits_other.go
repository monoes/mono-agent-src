//go:build windows

package finish

// No criterion is run on Windows (the mode does not start there), so there is no helper.
func limitsArgv(Limits, string, []string) (string, []string, bool) { return "", nil, false }

// RunLimitsHelperIfRequested does nothing: there is no helper on Windows.
func RunLimitsHelperIfRequested([]string) {}
