package autoinstall

import (
	"errors"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/automation"
	"github.com/spf13/cobra"
)

// DomainFlags are --domains / --use-suggested-domains, shared by
// `automation export` and `action export`: the site.domains written into
// the exported copy only (ExportOptions.Domains).
type DomainFlags struct {
	domains      string
	useSuggested bool
}

// Register adds the flags to cmd.
func (f *DomainFlags) Register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.domains, "domains", "", "Set site.domains in the exported copy (comma-separated), e.g. for a legacy package that lists none")
	cmd.Flags().BoolVar(&f.useSuggested, "use-suggested-domains", false, "For a legacy package: use the domains suggested from its actions")
}

// Resolve returns the domains to export with (nil = the package's own).
func (f *DomainFlags) Resolve(reg *automation.Registry, id string) ([]string, error) {
	list := splitCSV(f.domains)
	if !f.useSuggested {
		return list, nil
	}
	if len(list) > 0 {
		return nil, errors.New("--domains and --use-suggested-domains are mutually exclusive")
	}
	p, err := reg.Get(id)
	if err != nil {
		return nil, err
	}
	l := p.Manifest.Legacy
	switch {
	case l == nil:
		return nil, fmt.Errorf("--use-suggested-domains: %s is not a generated legacy package; pass --domains", id)
	case len(l.SuggestedDomains) > 0:
		return append([]string{}, l.SuggestedDomains...), nil
	case len(l.LocalHosts) > 0:
		return nil, fmt.Errorf("--use-suggested-domains: %s only opens local addresses (%s), which no exported package may list", id, strings.Join(l.LocalHosts, ", "))
	}
	return nil, fmt.Errorf("--use-suggested-domains: %s has no suggested domains (its actions open no literal URL); pass --domains", id)
}
