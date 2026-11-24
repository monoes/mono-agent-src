package crashreport

import (
	"os"
	"regexp"
	"strings"
)

// The patterns are deliberately broad: a crash record is filed to a public
// repository, so over-redacting is the safe direction.
var (
	emailRe  = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	bearerRe = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=\-]{8,}`)
	paramRe  = regexp.MustCompile(`(?i)([?&"'\s]|^)((?:access_|refresh_|id_)?token|api[_-]?key|apikey|secret|password|passwd|client_secret|code)=[^&\s"']+`)
	prefixRe = regexp.MustCompile(`\b(?:sk-|ghp_|gho_|ghs_|github_pat_|xox[abprs]-|AKIA)[A-Za-z0-9_\-]{12,}`)
	jwtRe    = regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]*`)
)

// Scrub removes what a panic message or stack must not carry off the machine:
// the home directory (replaced by ~), email addresses and token-looking values.
func Scrub(s string) string {
	if home, err := os.UserHomeDir(); err == nil && len(home) > 1 {
		s = strings.ReplaceAll(s, home, "~")
		if alt := strings.ReplaceAll(home, `\`, "/"); alt != home {
			s = strings.ReplaceAll(s, alt, "~")
		}
	}
	s = bearerRe.ReplaceAllString(s, "$1 [redacted]")
	s = paramRe.ReplaceAllString(s, "$1$2=[redacted]")
	s = jwtRe.ReplaceAllString(s, "[redacted]")
	s = prefixRe.ReplaceAllString(s, "[redacted]")
	return emailRe.ReplaceAllString(s, "[email]")
}
