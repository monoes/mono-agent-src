package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// The guards of issue 1333: an operator-only command asks the turn-aware gate (opgate), and a new one
// cannot slip past it. Three guards, each with a list that shrinks as the remaining verbs are converted:
//
//	T1 no source outside internal/cli/opgate (and internal/operator) asks the marker-only check
//	   (operator.Refusal, Marker, MarkerEnv, Caller, NotUser, AIRun, ApprovedWorkflowChild, UnapprovedRunProcess,
//	   hilsend.Detect, browserctl.AgentCaller and AgentKind, notOperator, IsAgentContext)
//	   except where a reason says why; an entry that begins "FINDING" is a marker-only decision still to convert;
//	T2 every command whose help carries the operator-only text has a row of operatorOnlyRows or a
//	   pending entry with a reason, and the cmOperatorOnly families of the client-mode table are each
//	   accounted for;
//	T3 (gate_operator_only_contexts_test.go) every row is refused in every context that is not the operator.

const (
	turnAware = "already behind the turn-aware gate (requireOperator, personGate)"
	classC    = "class C: no local operator check; whether it should refuse an agent locally is not decided (issue 1333)"
	pr4       = "converted in PR 4 (profile and people)"
	pr6c      = "converted in PR 6c (mode switch): open to agents for a built-in package, so no row; login_turn_gate_test.go holds its contexts"
	heldB     = "held: class B, the verb has an agent path next to the operator's; waits for the owner's answers (issue 1333)"
	gateImp   = "the gate's own implementation"
	finding   = "FINDING, a marker-only decision (issue 1333 audit "
)

// Limits of T1: it reads one file at a time, so it cannot see a wrapper in another package (callers of
// browserctl.RequestedBy or ContextKey), a direct os.Getenv("MONOAGENT_ACTOR"), or a value reference such as
// `var f = AgentCaller`; and its counts are per file, so swapping a label for a decision inside an
// allow-listed file keeps the count.

type allowed struct {
	n   int
	why string
}

// legacyMarkerChecks is the T1 allow-list: file (slash-separated from the repo root) to the number of
// occurrences and the reason. A new occurrence, or an entry that no longer matches, fails the test.
var legacyMarkerChecks = map[string]allowed{
	"cmd/monoagentcli/hil.go":                       {3, gateImp + " (notOperator in gateVerdict)"},
	"internal/cli/org/sign.go":                      {2, "class C: org sign has its own signature guard; not decided (issue 1333)"},
	"internal/jev/jevconf/key.go":                   {1, "library layer: cannot import opgate; the TYPESAFE_BASE_URL override is honoured only for the operator's own terminal: marker (operator.Refusal) + turn verdict (operator.InTurn) + unapproved ctx"},
	"internal/nodes/agent_run_gate.go":              {1, "engine node: the marker half of the run gate next to node run's verdict (workflow.AgentCallerFrom), not a CLI verb gate"},
	"internal/nodes/browserjev/gate.go":             {2, "engine node gate (IsAgentContext) not a CLI verb gate, the caller's kind for the card (AgentCaller), and UnapprovedRunProcess, which only adds a hold (fails closed)"},
	"internal/nodes/publication/register.go":        {2, "engine node: the marker half of a run's verdict next to the command's (workflow.AgentCallerFrom), not a CLI verb gate; operator.AIRun is the same marker-plus-run-marks decision as " + "aitask and people.save (issue 1333 audit G1c)"},
	"cmd/monoagentcli/task.go":                      {5, "task verbs have their own gate (personGate, NotOperator); taskCallerDeps stays marker-only for the gate's own steps, taskDeps swaps in the turn-aware operator.NotUser and taskAddedBy (operator.Caller names a marked caller)"},
	"internal/cli/task/task.go":                     {2, "task verbs have their own gate (personGate, NotOperator)"},
	"internal/browserctl/browserctl.go":             {1, "Open takes the permission policy from AgentCaller, which is turn-aware since the browser policy PR (#1472): a cleared-marker turn gets the agent level, and Open asks the caller once for the policy and the session key"},
	"internal/browserctl/context.go":                {5, "definitions and labels, no decision: IsAgentContext and its hilsend.Detect, RequestedBy (names the asker on a HIL card), ContextKey (names the session state file)"},
	"internal/browserctl/kindtoken.go":              {1, "ApplyTaskID reads AgentKind, which stays marker-only on purpose (it names a marked apply kind that a token vouches for; the policy verdict is AgentCaller, turn-aware since #1472): a label of the kind, not a permission decision"},
	"internal/browserctl/permissions.go":            {3, "definitions of AgentCaller and AgentKind and the IsAgentContext they read: the browser permission engine, not a CLI verb gate; the decisions that use them are listed at their call sites"},
	"cmd/monoagentcli/browser_applyscope.go":        {1, "browserTaskSession confines the apply kind by AgentKind: a marked apply kind is vouched by a token (kindtoken); AgentKind stays marker-only on purpose, the browser policy itself is AgentCaller, turn-aware since #1472"},
	"cmd/monoagentcli/browser_permissions.go":       {1, "display only: the \"you\" row of `browser permissions` names the caller's kind; it grants and withholds nothing"},
	"cmd/monoagentcli/browser_tabs.go":              {1, "`browser browsers` refuses level off by AgentCaller, which is turn-aware since #1472"},
	"internal/cli/node/run.go":                      {1, "exempts only an approved workflow's own child: ApprovedWorkflowChild requires an approved, live per-run token (#1427), so a self-set MONOAGENT_WORKFLOW_RUN or a cleared marker is not exempted; the verdict is not marker-only"},
	"internal/cli/people/messages_compose.go":       {1, "exempts only an approved workflow's own child: ApprovedWorkflowChild requires an approved, live per-run token (#1427), so a self-set MONOAGENT_WORKFLOW_RUN or a cleared marker is not exempted; the verdict is not marker-only"},
	"internal/hilsend/gate.go":                      {2, finding + "G1d): Guard queues a send by Detect's env markers (the definition is the other occurrence); an unapproved run is still caught by unapprovedRun"},
	"internal/nodes/aitask/node.go":                 {1, finding + "G1c): the aitask node refuses an AI run by operator.AIRun, which asks Marker; a workflow run is not marked as an AI caller"},
	"internal/nodes/control/human_in_loop_tags.go":  {1, "label, not a decision: Detect names the org and chat an item is filed under; it grants and withholds nothing"},
	"internal/nodes/people/save.go":                 {1, finding + "G1c): people.save approves outreach for a run that is not an AI run by operator.AIRun (marker plus the run's marks)"},
	"internal/cli/workflowaitask/workflowaitask.go": {1, "label only, not a decision: the audit line names a marked caller (operator.Caller), \"operator\" otherwise; it grants and withholds nothing (issue 1425)"},
	"internal/config/writeguard.go":                 {1, "reserved config writes, engine level"},
}

func TestNoMarkerOnlyOperatorChecksOutsideTheGate(t *testing.T) {
	found := map[string]int{}
	for _, base := range []string{"cmd", "internal"} {
		root := filepath.Join("..", "..", base)
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(filepath.Join("..", ".."), path)
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if rel == "internal/operator" || rel == "internal/cli/opgate" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			found[rel] += countMarkerOnlyChecks(t, rel, b)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	var bad []string
	for f, n := range found {
		if n == 0 {
			continue
		}
		switch a, ok := legacyMarkerChecks[f]; {
		case !ok:
			bad = append(bad, f+": a marker-only operator check outside opgate, and not on the allow-list; ask opgate.Refusal instead")
		case a.n != n:
			bad = append(bad, f+": the allow-list says "+strconv.Itoa(a.n)+" occurrence(s), the source has "+strconv.Itoa(n))
		}
	}
	for f, a := range legacyMarkerChecks {
		if a.why == "" {
			bad = append(bad, f+": an allow-list entry needs a reason")
		}
		if found[f] == 0 {
			bad = append(bad, f+": on the allow-list, but the source no longer has such a check; remove the entry")
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Fatalf("legacy operator checks and the allow-list disagree:\n%s", strings.Join(bad, "\n"))
	}
}

// pendingOperatorOnly lists, with a reason, the commands that carry the operator-only help text and are
// not rows of operatorOnlyRows yet. Converting a command moves it to the table.
var pendingOperatorOnly = map[string]string{
	"login": pr6c,
}

// turnAwareCommands carry the help text and were behind the turn-aware gate before the table.
var turnAwareCommands = map[string]string{
	"application fill queue":  turnAware,
	"application fill resume": turnAware,
	"application fill sweep":  turnAware,
	"chat history set-access": turnAware,
	"coder set":               turnAware,
	"workflow apply-fix":      turnAware,
	"workflow save-draft":     turnAware,
}

// verbsWithoutTheHelpText are rows whose command does not carry the help text (the gate is in the code,
// not in the help); the guard still needs them to be real commands.
var verbsWithoutTheHelpText = map[string]bool{
	"secret agent-access": true, "secret agent-access audit": true, "secret export": true,
	"connect get-oauth-client": true, "profile info get": true, "workflow run": true,
	"extension pair": true, "extension install": true, "org role set-access": true,
	"automation trust": true, "automation rerecord": true, "automation doctor accept-repair": true,
	"record verify": true, "record replay": true, "browser allow add": true, "browser allow remove": true,
	"browser permissions set": true, "browser permissions site": true,
	"coder queue add": true, "coder queue start": true, "coder projects map": true, "coder projects unmap": true,
	"profile update": true, "profile delete": true, "profile backups purge": true, "profile move": true,
	"profile create": true, "profile documents remove": true, "people review approve": true,
}

func treeKeys(t *testing.T) (all map[string]bool, withHelp []string) {
	t.Helper()
	root := newRootCmd()
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	all = map[string]bool{}
	for _, c := range allCommands(root) {
		all[commandKey(c)] = true
		if strings.Contains(c.Long, clicore.OperatorOnlyHelp) {
			withHelp = append(withHelp, commandKey(c))
		}
	}
	sort.Strings(withHelp)
	return all, withHelp
}

func TestEveryOperatorOnlyCommandHasARowOrAPendingReason(t *testing.T) {
	all, withHelp := treeKeys(t)
	if len(withHelp) < 20 {
		t.Fatalf("only %d commands carry the operator-only help text: the guard's marker is stale", len(withHelp))
	}
	rows := map[string]bool{}
	for _, r := range operatorOnlyRows() {
		rows[r.path] = true
	}
	var bad []string
	for _, k := range withHelp {
		if !rows[k] && pendingOperatorOnly[k] == "" && turnAwareCommands[k] == "" {
			bad = append(bad, k+": carries the operator-only help text, has no row in operatorOnlyRows and no pending reason")
		}
	}
	for k := range rows {
		if !all[k] {
			bad = append(bad, k+": a row for a command that is not in the tree")
		}
	}
	for k := range turnAwareCommands {
		if !all[k] || rows[k] {
			bad = append(bad, k+": in turnAwareCommands, but not a command of the tree (or also a row)")
		}
	}
	for k, why := range pendingOperatorOnly {
		if !all[k] {
			bad = append(bad, k+": pending, but not a command in the tree")
		}
		if why == "" || rows[k] {
			bad = append(bad, k+": pending needs a reason and must not also be a row")
		}
	}
	for _, r := range operatorOnlyRows() {
		help := false
		for _, k := range withHelp {
			help = help || k == r.path
		}
		if !help && !verbsWithoutTheHelpText[r.path] {
			bad = append(bad, r.path+": a row whose command has no operator-only help text and is not in verbsWithoutTheHelpText")
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Fatalf("operatorOnlyRows, the pending list and the command tree disagree:\n%s", strings.Join(bad, "\n"))
	}
}

// Each row's build and args reach the command its path names.
func TestEveryOperatorOnlyRowReachesItsCommand(t *testing.T) {
	for _, r := range operatorOnlyRows() {
		var verb []string
		for _, a := range r.args {
			if strings.HasPrefix(a, "-") {
				break
			}
			verb = append(verb, a)
		}
		c, _, err := r.build(&globalConfig{}).Find(verb)
		if err != nil {
			t.Errorf("%s: %v", r.path, err)
			continue
		}
		if !strings.HasSuffix(r.path, " "+c.Name()) {
			t.Errorf("%s: the row's args reach %q", r.path, c.Name())
		}
	}
}

// noLocalGate are the cmOperatorOnly families of the client-mode table with no local operator check at
// all (class C of issue 1333: whether they should refuse an agent locally is not decided).
var noLocalGate = map[string]string{
	"account":     classC,
	"daemon":      classC,
	"db":          classC,
	"httpapi":     classC,
	"init":        classC,
	"integration": classC,
	"library":     classC,
	"logout":      classC,
	"mcp":         classC,
	"release":     classC,
	"setup":       classC,
	"update":      classC,
}

// alreadyTurnAware are cmOperatorOnly families whose verbs were put behind the turn-aware gate before
// this table (requireOperator, personGate).
var alreadyTurnAware = map[string]string{
	"aitask budget":      turnAware,
	"aitask start":       turnAware,
	"aitask submit":      turnAware,
	"aitask submit-page": turnAware,
	"aitask supervise":   turnAware,
	"aitask wait":        turnAware,
	"coder":              turnAware,
	"hil":                turnAware,
	"sandbox":            turnAware,
}

// Every cmOperatorOnly key of the client-mode table is accounted for: a row, a pending command, a family
// without a local gate, or already behind the turn-aware gate. Adding a cmOperatorOnly command forces the decision.
func TestEveryClientModeOperatorOnlyFamilyIsAccountedFor(t *testing.T) {
	covers := func(key, path string) bool { return path == key || strings.HasPrefix(path, key+" ") }
	var bad []string
	for key, class := range clientModeClasses {
		if class != cmOperatorOnly {
			continue
		}
		ok := noLocalGate[key] != "" || alreadyTurnAware[key] != ""
		for _, r := range operatorOnlyRows() {
			ok = ok || covers(key, r.path)
		}
		for p := range pendingOperatorOnly {
			ok = ok || covers(key, p)
		}
		if !ok {
			bad = append(bad, key+": operator-only in the client-mode table, but no row, no pending command, no noLocalGate and no alreadyTurnAware entry")
		}
	}
	for k := range noLocalGate {
		if c, ok := clientModeClasses[k]; !ok || c != cmOperatorOnly {
			bad = append(bad, k+": in noLocalGate, but not an operator-only key of the client-mode table")
		}
	}
	for k := range alreadyTurnAware {
		if c, ok := clientModeClasses[k]; !ok || c != cmOperatorOnly {
			bad = append(bad, k+": in alreadyTurnAware, but not an operator-only key of the client-mode table")
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Fatalf("clientModeClasses and the operator-only tables disagree:\n%s", strings.Join(bad, "\n"))
	}
}
