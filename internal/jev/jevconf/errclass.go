package jevconf

import (
	"context"
	"errors"
	"regexp"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/secrets"
)

// httpStatus finds an adapter error's HTTP status ("…: HTTP 503 …").
var httpStatus = regexp.MustCompile(`\bHTTP ([1-5][0-9][0-9])\b`)

// ErrorClass names the class of a decision call's error in a few fixed words,
// for text that is stored where agents read it (an org decision's rationale,
// a Human-in-Loop row): a provider's own error text may carry what its
// server answered, which is never quoted there. It is one of "timeout",
// "canceled", "refused", "no key", "unsupported", "invalid answer",
// "HTTP <status>" or "error".
func ErrorClass(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, secrets.ErrResolveRefused), errors.Is(err, ErrOperatorOnly), errors.Is(err, ErrExplicitKeyWithProvider),
		errors.Is(err, ErrKeylessRemote), errors.Is(err, ErrProviderURL):
		return "refused"
	case errors.Is(err, jev.ErrNoAPIKey):
		return "no key"
	case errors.Is(err, jev.ErrUnsupported):
		return "unsupported"
	case errors.Is(err, jev.ErrInvalidAnswer):
		return "invalid answer"
	}
	if m := httpStatus.FindStringSubmatch(err.Error()); m != nil {
		return "HTTP " + m[1]
	}
	return "error"
}
