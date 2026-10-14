package workflowbundle

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"slices"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Workflow bundles (spec §5.3): `workflow export --bundle-automations`
// embeds the automation packages the workflow's nodes use, so a shared
// workflow works on import.
//
// Format: the ordinary WorkflowFile JSON plus one optional top-level field,
//
//	"automations": {
//	  "<id>": {"version": "1.2.0", "sha256": "<hex of the .mpkg>", "mpkg": "<base64 .mpkg>"}
//	}
//
// The field is ignored by importers that predate it (the workflow parser
// skips unknown keys), so bundled files stay importable everywhere. A node
// uses automation <id> when its type's prefix resolves to installed package
// <id> on the exporting machine (directly or through a legacy alias); in a
// bundle such node types are written as "<id>.<action>".

// BundledAutomation is one entry of the "automations" field.
type BundledAutomation struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Mpkg    string `json:"mpkg"` // base64 (std encoding) of the .mpkg zip
}

// BundleFile is a WorkflowFile with bundled automations.
type BundleFile struct {
	workflow.WorkflowFile
	Automations map[string]BundledAutomation `json:"automations,omitempty"`
	// Unbundled lists packages the workflow uses that could not be exported
	// (e.g. a legacy package whose sites could not be worked out). The
	// workflow and every exportable package are still written; the
	// importing side reports these as missing, with the reason and hint.
	Unbundled map[string]UnbundledAutomation `json:"unbundledAutomations,omitempty"`
	// SuggestedUsed: packages exported with their suggested domains
	// (--use-suggested-domains), for the exporter's notice. Not written.
	SuggestedUsed map[string][]string `json:"-"`
}

// UnbundledAutomation says why a used package is not in the bundle.
type UnbundledAutomation struct {
	Version string `json:"version,omitempty"`
	Reason  string `json:"reason"`
	Hint    string `json:"hint,omitempty"` // "" when no export option can help
	// LocalOnly: the package opens local addresses (localhost, 127.0.0.1,
	// …), which no exported package may allow; it can only run where it was
	// made, so the recipient has to recreate it.
	LocalOnly bool `json:"localOnly,omitempty"`
}

// Options controls which site domains an exported package gets.
// Generated legacy packages run unrestricted locally and have no
// site.domains; an exported copy must name them (Registry.Export refuses
// otherwise).
type Options struct {
	// Domains sets site.domains of the exported copy per automation id
	// (--automation-domains <id>=<site,...>).
	Domains map[string][]string
	// UseSuggested uses a legacy package's suggested domains (derived from
	// its literal navigate URLs) when no explicit domains are given.
	UseSuggested bool
	// SkipShipped leaves out built-in and official monoes.me packages
	// (built-in trust): `library publish` relies on the library to install
	// those, at their own trust, instead of copying them as imported.
	SkipShipped bool
}

// ParseAutomationDomains parses --automation-domains values "<id>=<a,b>".
func (d Deps) ParseAutomationDomains(vals []string) (map[string][]string, error) {
	out := map[string][]string{}
	for _, v := range vals {
		id, list, ok := strings.Cut(v, "=")
		id = strings.TrimSpace(id)
		if !ok || id == "" || len(splitCSV(list)) == 0 {
			return nil, d.InvalidInput("--automation-domains %q: want <automation id>=<site,...>", v)
		}
		out[id] = append(out[id], splitCSV(list)...)
	}
	return out, nil
}

// BundleWorkflowAutomations exports every installed automation the
// workflow's nodes use into the file's "automations" field. A package that
// cannot be exported is listed under "unbundledAutomations" instead; the
// export as a whole never fails because of one package.
func (d Deps) BundleWorkflowAutomations(file workflow.WorkflowFile, opts Options) (BundleFile, error) {
	out := BundleFile{WorkflowFile: file}
	reg, err := d.OpenRegistry()
	if err != nil {
		return out, err
	}
	resolve := PackageResolver(reg)
	ids := WorkflowAutomationIDs(file.Nodes, resolve)
	if err := d.checkDomainIDs(opts.Domains, ids); err != nil {
		return out, err
	}
	for _, id := range ids {
		info, err := reg.Info(id)
		if err != nil || (opts.SkipShipped && info.Trust == automation.TrustBuiltin) {
			continue
		}
		var buf bytes.Buffer
		domains, suggested := exportDomains(reg, id, opts)
		if err := reg.Export(id, &buf, automation.ExportOptions{Domains: domains}); err != nil {
			if out.Unbundled == nil {
				out.Unbundled = map[string]UnbundledAutomation{}
			}
			if hosts := localHosts(reg, id); len(hosts) > 0 {
				out.Unbundled[id] = UnbundledAutomation{Version: info.Version, LocalOnly: true,
					Reason: fmt.Sprintf("opens a local address (%s) — it can only run on this machine; the recipient has to create it themselves",
						strings.Join(hosts, ", "))}
				continue
			}
			out.Unbundled[id] = UnbundledAutomation{Version: info.Version, Reason: err.Error(), Hint: unbundledHint(reg, id)}
			continue
		}
		if suggested {
			if out.SuggestedUsed == nil {
				out.SuggestedUsed = map[string][]string{}
			}
			out.SuggestedUsed[id] = domains
		}
		sum := sha256.Sum256(buf.Bytes())
		if out.Automations == nil {
			out.Automations = map[string]BundledAutomation{}
		}
		out.Automations[id] = BundledAutomation{
			Version: info.Version,
			SHA256:  hex.EncodeToString(sum[:]),
			Mpkg:    base64.StdEncoding.EncodeToString(buf.Bytes()),
		}
	}
	out.Nodes = CanonicalNodeTypes(file.Nodes, resolve, out.Automations)
	return out, nil
}

// exportDomains picks site.domains for the exported copy of id: explicit
// ones first, else (opt-in) a legacy package's suggestions — only when it
// opens no local host, which no exported package may allow — else none
// (the package's own). suggested reports that the suggestions were used.
func exportDomains(reg *automation.Registry, id string, opts Options) (domains []string, suggested bool) {
	if d := opts.Domains[id]; len(d) > 0 {
		return d, false
	}
	if opts.UseSuggested {
		if p, err := reg.Get(id); err == nil && p.Manifest.Legacy != nil &&
			len(p.Manifest.Legacy.SuggestedDomains) > 0 && len(p.Manifest.Legacy.LocalHosts) == 0 {
			return p.Manifest.Legacy.SuggestedDomains, true
		}
	}
	return nil, false
}

// unbundledHint tells the user how to include id next time.
func unbundledHint(reg *automation.Registry, id string) string {
	if p, err := reg.Get(id); err == nil && p.Manifest.Legacy != nil && len(p.Manifest.Legacy.SuggestedDomains) > 0 {
		return fmt.Sprintf("re-run workflow export with --automation-domains %s=%s (or --use-suggested-domains), or share it separately: monoagentcli automation export %s --domains %s",
			id, strings.Join(p.Manifest.Legacy.SuggestedDomains, ","), id, strings.Join(p.Manifest.Legacy.SuggestedDomains, ","))
	}
	return fmt.Sprintf("re-run workflow export with --automation-domains %s=<site,...> naming the sites its actions open, or share it separately: monoagentcli automation export %s --domains <site,...>", id, id)
}

// SuggestedNotices are human lines for packages exported with their
// suggested domains: the recipient's install review will show them.
func SuggestedNotices(b BundleFile) []string {
	ids := make([]string, 0, len(b.SuggestedUsed))
	for id := range b.SuggestedUsed {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var lines []string
	for _, id := range ids {
		lines = append(lines, fmt.Sprintf("note: bundled %s with suggested domains %s (the recipient's install review shows them)",
			id, strings.Join(b.SuggestedUsed[id], ", ")))
	}
	return lines
}

// UnbundledWarnings are human lines for the packages left out of a bundle.
func UnbundledWarnings(b BundleFile) []string {
	ids := make([]string, 0, len(b.Unbundled))
	for id := range b.Unbundled {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var lines []string
	for _, id := range ids {
		u := b.Unbundled[id]
		line := fmt.Sprintf("warning: automation %s not bundled: %s", id, u.Reason)
		if u.Hint != "" {
			line += "\n  " + u.Hint
		}
		lines = append(lines, line)
	}
	return lines
}

// localHosts returns the local addresses package id opens: a legacy
// package's recorded local hosts, or local entries of site.domains.
// Exporting cannot fix these (no exported package may allow them).
func localHosts(reg *automation.Registry, id string) []string {
	p, err := reg.Get(id)
	if err != nil {
		return nil
	}
	if p.Manifest.Legacy != nil && len(p.Manifest.Legacy.LocalHosts) > 0 {
		return p.Manifest.Legacy.LocalHosts
	}
	var out []string
	for _, d := range p.Manifest.Site.Domains {
		if IsLocalAddress(d) {
			out = append(out, d)
		}
	}
	return out
}

// IsLocalAddress reports whether host[:port] names this machine or a
// private network: localhost, *.localhost, *.local, loopback and private
// IP addresses.
func IsLocalAddress(hostport string) bool {
	host := strings.ToLower(strings.TrimSpace(hostport))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast())
}

// checkDomainIDs refuses --automation-domains for an id this export does
// not use: a typo would otherwise silently leave the package unbundled.
func (d Deps) checkDomainIDs(domains map[string][]string, used []string) error {
	var unknown []string
	for id := range domains {
		if !slices.Contains(used, id) {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	list := strings.Join(used, ", ")
	if list == "" {
		list = "none"
	}
	return d.InvalidInput("--automation-domains %s: no such automation in this export (bundled: %s)",
		strings.Join(unknown, ", "), list)
}
