package automation

import (
	"io/fs"

	"github.com/monoes/mono-agent/data"
)

// automationTemplatesRoot is data/automation-templates as an fs.FS: one
// directory per template with {{id}}, {{name}}, {{startUrl}} and {{domain}}
// placeholders.
func automationTemplatesRoot() (fs.FS, error) {
	return fs.Sub(data.TemplatesFS, "automation-templates")
}
