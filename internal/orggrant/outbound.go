package orggrant

import (
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/workflow"
)

// OutboundNodes lists the nodes of wf that may act on the outside world or
// can't be shown not to. A grant to a workflow with any of them defaults to
// approval "required" and tier irreversible (C-34), so a person approves
// its calls even at autonomy mid.
//
// The classifier is deny-by-default: only the node types in readOnlyNodes,
// the config-dependent reads in readOnlyByConfig, and the official read
// actions in readOnlyActions whose installed definition checks out are not
// outbound. Every other type counts, including unknown and future ones and
// every other installed automation package's actions. A false positive costs one extra
// decision, a false negative an unreviewed side effect.
func OutboundNodes(wf *workflow.Workflow) []string {
	if wf == nil {
		return nil
	}
	var out []string
	for _, n := range wf.Nodes {
		if n.Disabled {
			continue
		}
		if isOutboundNode(n) {
			label := n.Type
			if n.Name != "" {
				label = n.Name + " (" + n.Type + ")"
			}
			out = append(out, label)
		}
	}
	sort.Strings(out)
	return out
}

func isOutboundNode(n workflow.WorkflowNode) bool {
	t := n.Type
	if readOnlyNodes[t] {
		return false
	}
	if read, ok := readOnlyByConfig[t]; ok {
		return !read(n.Config)
	}
	if methods, ok := readOnlyActions[t]; ok {
		return !installedActionReadOnly(t, methods)
	}
	return true
}

// readOnlyNodes are the built-in node types reviewed as reading or shaping
// data without acting on the outside world, each checked against its
// implementation. Keep this list exact: no prefix or suffix rules, since
// installed automation packages choose their own action names (and may not
// use a built-in namespace as their id: automation.ReservedID).
var readOnlyNodes = setOf(
	// Triggers the engine passes through without running anything.
	"trigger.manual", "trigger.org", "trigger.schedule", "trigger.webhook",
	// Control flow and data shaping. core.code runs goja with only $input
	// and $json bound; core.human_in_loop waits for a person.
	"core.aggregate", "core.code", "core.compare_datasets", "core.filter",
	"core.human_in_loop", "core.if", "core.limit", "core.merge",
	"core.remove_duplicates", "core.set", "core.sort", "core.split_in_batches",
	"core.stop_error", "core.switch", "core.wait",
	"data.compression", "data.crypto", "data.datetime", "data.html",
	"data.markdown", "data.xml",
	// Image transforms write a new derived file; image.info only reads.
	// remove_background downloads its model with a GET.
	"image.adjust", "image.convert", "image.crop", "image.info",
	"image.remove_background", "image.resize", "image.thumbnail",
	"image.vault_get", "vault.secret_get",
	// Reads over the network.
	"comm.email_read", "comm.outlook_read", "system.rss_read",
	"ai.read_page", // static GET, SSRF-guarded, no browser
	// AI with no tools: ai.choose is a TypeSafe Jev classification (its
	// input goes to TypeSafe's API). The other ai.* are deprecated stubs
	// that fail without doing anything (ai.agent is left out regardless).
	"ai.choose",
	"ai.chat", "ai.classify", "ai.embed", "ai.extract", "ai.transform",
	// Bookkeeping in mono-agent's own stores: nothing leaves the machine
	// and nothing is overwritten beyond the rows the node owns.
	// discovery.search_jobs reads public job boards with GETs.
	"people.save", "people.sync_outlook_message",
	"applications.create", "applications.list",
	"applications.prepare", "applications.set_status", "applications.tag",
	"discovery.search_jobs", "documents.render", "image.vault_save", "publication.register",
	// Legacy unprefixed names (noderegistry.Build's aliases) of the above.
	"aggregate", "code", "filter", "if", "limit", "merge", "set", "sort",
	"switch", "wait", "compression", "crypto", "datetime", "html",
	"markdown", "xml", "email_read", "rss_read",
)

// readOnlyActions are the official browser automations' read actions
// (automations/*/actions, sideEffects "read"), each with the native bot
// methods it calls. The name alone is not trusted: the action runs from an
// installed package that can be updated or edited, so installedActionReadOnly
// checks the installed definition every time and fails closed.
var readOnlyActions = map[string][]string{
	"hackernews.get_post_metrics":    nil,
	"hackernews.list_comments":       nil,
	"instagram.export_followers":     {"fetch_followers_list"},
	"instagram.extract_post_data":    {"scrape_post_data"},
	"instagram.find_by_keyword":      {"find_post_authors"},
	"instagram.list_post_comments":   {"list_post_comments"},
	"instagram.list_user_posts":      {"list_user_posts"},
	"instagram.scrape_profile_info":  {"get_user_info"},
	"linkedin.export_followers":      {"list_followers"},
	"linkedin.find_by_keyword":       {"search_people"},
	"linkedin.list_post_comments":    {"list_post_comments"},
	"linkedin.list_user_posts":       {"list_user_posts"},
	"linkedin.scrape_profile_info":   {"get_profile_data"},
	"producthunt.get_launch_metrics": {"get_launch_metrics"},
	"producthunt.list_comments":      {"list_comments"},
	"tiktok.export_followers":        {"list_followers"},
	"tiktok.find_by_keyword":         {"search_videos"},
	"tiktok.list_user_videos":        {"list_user_videos"},
	"tiktok.list_video_comments":     {"list_video_comments"},
	"tiktok.scrape_profile_info":     {"get_profile_data"},
	"x.export_followers":             {"list_followers"},
	"x.find_by_keyword":              {"search_posts"},
	"x.scrape_profile_info":          {"get_profile_data"},
}

// readStepTypes are the action step types that only navigate, read the
// page or keep run state. Anything else (click, type, page_script, upload,
// download, http_fetch_in_page, call_action, …) fails the check.
var readStepTypes = setOf(
	"navigate", "wait", "extract_text", "extract_attribute", "extract_table",
	"condition", "transform", "set_variable", "save_data", "log",
	"update_progress", "mark_failed",
)

// installedActionReadOnly reports whether the installed definition of the
// automation action nodeType still only reads: an official ("builtin"
// trust) package, sideEffects read or none, and every step (nested and in
// called fragments) a read step or one of methods, none marked sideEffect.
// Anything it cannot load or verify counts as not read-only.
func installedActionReadOnly(nodeType string, methods []string) bool {
	pkgID, name, _ := strings.Cut(nodeType, ".")
	src := action.CurrentDefSource()
	if src == nil {
		return false
	}
	pkg := src.Package(pkgID)
	if pkg == nil || action.PackageTrust(pkg) != "builtin" {
		return false
	}
	def, err := action.GetLoader().Load(pkgID, name)
	if err != nil || def == nil {
		return false
	}
	if level := strings.ToLower(strings.TrimSpace(def.SideEffects)); level != "read" && level != "none" {
		return false
	}
	return stepsReadOnly(def.Steps, pkg, setOf(methods...), map[string]bool{})
}

func stepsReadOnly(steps []action.StepDef, pkg action.PackageContext, methods, seen map[string]bool) bool {
	for _, s := range steps {
		if s.SideEffect || !stepsReadOnly(s.Steps, pkg, methods, seen) {
			return false
		}
		switch {
		case readStepTypes[s.Type]:
		case s.Type == "call_bot_method":
			if !methods[s.MethodName] {
				return false
			}
		case s.Type == "call_fragment":
			if seen[s.Fragment] {
				continue
			}
			seen[s.Fragment] = true
			f, err := pkg.Fragment(s.Fragment)
			if err != nil || f == nil || !stepsReadOnly(f.Steps, pkg, methods, seen) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// readOnlyByConfig are node types that read or write depending on their
// config; the function reports whether this config only reads.
var readOnlyByConfig = map[string]func(map[string]interface{}) bool{
	"http.request":     httpReadOnly,
	"http_request":     httpReadOnly,
	"data.spreadsheet": spreadsheetReadOnly,
	"spreadsheet":      spreadsheetReadOnly,
}

// httpReadOnly: GET (the node's default) and HEAD. A templated method is
// not known until run time, so it counts as a write. GET is not harmless
// on the wire (some APIs act on it, and a query string can carry data
// out); it is still left to the consequential tier.
func httpReadOnly(cfg map[string]interface{}) bool {
	method, _ := cfg["method"].(string)
	return method == "" || strings.EqualFold(method, "GET") || strings.EqualFold(method, "HEAD")
}

func spreadsheetReadOnly(cfg map[string]interface{}) bool {
	op, _ := cfg["operation"].(string)
	return op == "read_csv" || op == "read_xlsx"
}

// outboundNodes and outboundPrefixes name the registered node types
// reviewed as outbound. isOutboundNode doesn't need them (everything off
// the read-only lists is outbound); they exist so the registry test can
// tell a reviewed outbound type from one nobody classified.
var outboundNodes = setOf(
	"agent.ask", "ai.agent", // agents that can run tools
	// Local agent turns in the workspace-write sandbox: nothing in the
	// node keeps the runtime from using its tools.
	"ai.extract_page", "applications.evaluate",
	"browser.jev", // goal-driven agent in the user's browser
	"http.ftp", "http.ssh", "system.execute_command",
	"data.write_binary_file", // writes any local path
	"vault.secret_save",      // overwrites credentials
)

var outboundPrefixes = []string{
	"comm.", "service.", "action.", "db.", "org.",
	// Browser automations; their read actions are in readOnlyActions.
	"gemini.", "hackernews.", "instagram.", "linkedin.", "producthunt.",
	"tiktok.", "x.",
}

// classifiedOutbound also covers the readOnlyActions, which are outbound
// whenever their installed definition fails the check.
func classifiedOutbound(t string) bool {
	if outboundNodes[t] {
		return true
	}
	for _, p := range outboundPrefixes {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

func setOf(types ...string) map[string]bool {
	m := make(map[string]bool, len(types))
	for _, t := range types {
		m[t] = true
	}
	return m
}

// GrantTier is the decision tier a call to a granted automation gets when
// it needs a decision (plan §7.7): irreversible with outbound nodes,
// consequential otherwise.
func GrantTier(outbound []string) string {
	if len(outbound) > 0 {
		return orgdesign.TierIrreversible
	}
	return orgdesign.TierConsequential
}
