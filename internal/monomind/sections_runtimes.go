package monomind

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Runtimes a sections org may use, as monomind's isolation registry
// (orgrt/documents/runtime-isolation.ts) sees them. monomind exposes the
// registry only through `org validate`, which reports each role's runtime:
//
//	roles.a: runtime "kilo" is refused for sections orgs: <reason>
//	roles.c: runtime "kimicode" uses the generic "config-env" isolation, not probed on a host that has the CLI (<note>)
//
// SectionsRuntimes asks monomind that way, so the answer follows the
// installed monomind. Only where that yields nothing (a monomind without
// the sections surface) does it use sectionsFallback, the one hardcoded copy.

// Sections runtime statuses. Runtimes that are fine are not listed.
const (
	SectionsRuntimeRefused    = "refused"    // cannot run in a sections org: block it
	SectionsRuntimeUnverified = "unverified" // allowed, with a warning
)

// SectionsRuntime is one runtime a sections org treats specially.
type SectionsRuntime struct {
	Runtime string `json:"runtime"`
	Status  string `json:"status"`
	// Reason is monomind's own text for why (the registry note).
	Reason string `json:"reason"`
}

// SectionsRuntimePolicy is the picker's answer: Source is "monomind" when
// read from `org validate`, "fallback" when from sectionsFallback.
type SectionsRuntimePolicy struct {
	Source   string            `json:"source"`
	Runtimes []SectionsRuntime `json:"runtimes"`
}

// sectionsFallback mirrors runtime-isolation.ts as of monomind 2.24.1: the
// refused runtimes and the unverified ones, for a monomind that cannot be
// asked. sections_runtimes_test.go pins it.
var sectionsFallback = []SectionsRuntime{
	{"kilo", SectionsRuntimeRefused, "Kilo supports full access only and a sections org refuses full access (a full-access role runs with no authority mask), so no role can run on it"},
	{"kimicode", SectionsRuntimeUnverified, ""},
	{"qwen", SectionsRuntimeUnverified, ""},
	{"qwen-rpc", SectionsRuntimeUnverified, ""},
	{"dsh", SectionsRuntimeUnverified, ""},
	{"aider", SectionsRuntimeUnverified, ""},
	{"cline", SectionsRuntimeUnverified, ""},
	{"vercel", SectionsRuntimeUnverified, ""},
}

const unverifiedFallbackReason = "its isolation has not been probed against a real install of this runtime"

var (
	sectionsRefusedRe    = regexp.MustCompile(`roles\.[^:\s]+: runtime "([^"]+)" is refused for sections orgs: (.+)$`)
	sectionsUnverifiedRe = regexp.MustCompile(`roles\.[^:\s]+: (runtime "([^"]+)" uses the generic .+)$`)
)

// ParseSectionsRuntimeFindings reads `org validate` output for the runtimes
// it refuses or flags as unverified, by runtime id.
func ParseSectionsRuntimeFindings(out string) map[string]SectionsRuntime {
	found := map[string]SectionsRuntime{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if m := sectionsRefusedRe.FindStringSubmatch(line); m != nil {
			found[m[1]] = SectionsRuntime{m[1], SectionsRuntimeRefused, m[2]}
		} else if m := sectionsUnverifiedRe.FindStringSubmatch(line); m != nil {
			found[m[2]] = SectionsRuntime{m[2], SectionsRuntimeUnverified, m[1]}
		}
	}
	return found
}

// sectionsProbeOrg is a sections org with one role per runtime (all under a
// claude root), for `org validate` to judge each runtime's isolation.
func sectionsProbeOrg(runtimes []string) ([]byte, error) {
	roles := []map[string]any{{"id": "root", "title": "Root", "runtime": "claude", "reports_to": nil}}
	var members []string
	for i, rt := range runtimes {
		id := fmt.Sprintf("r%d", i)
		members = append(members, id)
		roles = append(roles, map[string]any{"id": id, "title": id, "runtime": rt, "reports_to": "root"})
	}
	return json.Marshal(map[string]any{
		"name": "probe", "goal": "probe", "runtime": "claude", "roles": roles,
		"sections": map[string]any{"probe": map[string]any{"lead": members[0], "members": members}},
	})
}

// SectionsRuntimes returns the runtimes a sections org refuses or only
// warns about. Scan's own refusals (execution_supported false) are added
// with scan's reason.
func SectionsRuntimes(ctx context.Context) (*SectionsRuntimePolicy, error) {
	scan, err := Scan(ctx)
	if err != nil {
		return nil, err
	}
	var ids []string
	byID := map[string]SectionsRuntime{}
	for _, a := range scan.Agents {
		ids = append(ids, a.ID)
	}
	policy := &SectionsRuntimePolicy{Source: "monomind"}
	if len(ids) > 0 {
		if out := probeSectionsRuntimes(ctx, ids); len(out) > 0 {
			byID = out
		}
	}
	if len(byID) == 0 {
		policy.Source = "fallback"
		for _, f := range sectionsFallback {
			if f.Reason == "" {
				f.Reason = unverifiedFallbackReason
			}
			byID[f.Runtime] = f
		}
	}
	for _, a := range scan.Agents {
		if reason, refused := a.UnsupportedReason(); refused {
			cur := byID[a.ID]
			if reason == "" {
				reason = cur.Reason
			}
			byID[a.ID] = SectionsRuntime{a.ID, SectionsRuntimeRefused, reason}
		}
	}
	for _, r := range byID {
		policy.Runtimes = append(policy.Runtimes, r)
	}
	sort.Slice(policy.Runtimes, func(i, j int) bool { return policy.Runtimes[i].Runtime < policy.Runtimes[j].Runtime })
	return policy, nil
}

// probeSectionsRuntimes validates a throwaway sections org in a temp
// project; nil when monomind could not be asked.
func probeSectionsRuntimes(ctx context.Context, ids []string) map[string]SectionsRuntime {
	doc, err := sectionsProbeOrg(ids)
	if err != nil {
		return nil
	}
	dir, err := os.MkdirTemp("", "monoagent-sections-probe-")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(dir)
	orgs := filepath.Join(dir, ".monomind", "orgs")
	if os.MkdirAll(orgs, 0o755) != nil || os.WriteFile(filepath.Join(orgs, "probe.json"), doc, 0o644) != nil {
		return nil
	}
	// A refused role makes validate exit non-zero; its findings are in the
	// error text, so both outputs are read.
	out, err := runOrgText(ctx, dir, "validate", "probe")
	if err != nil {
		out += "\n" + err.Error()
	}
	return ParseSectionsRuntimeFindings(out)
}
