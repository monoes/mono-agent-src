// Package all links every decision-provider adapter into a binary: each
// adapter registers its kind with jevconf.RegisterKind from init(). Import it
// blank wherever decision providers must be available.
package all

import (
	_ "github.com/monoes/mono-agent/internal/jev/providers/openaicompat"
)
