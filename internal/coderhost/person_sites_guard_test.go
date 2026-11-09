package coderhost

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The pursuit loop's away limits reset on a person's action, and a person's send is recognised by the reserved turn-id
// prefixes (answer-p-, ask-p-) and, for a plain chat send, by having no issuer and no host-made turn id (see
// pursuit_person.go). Anything that queues a command, or calls a helper that speaks for the person, anywhere in
// internal/ or cmd/, could therefore reset the limits. This guard lists every such site; an unlisted one fails and
// points at the review. Reviewed in the S4 pursuit loop review (wave-s4-sec2, wave-s4-sec3), issue #1548.

var insertCommands = regexp.MustCompile(`(?i)INSERT\s+(?:OR\s+\w+\s+)?INTO\s+coder_session_commands`)

const coderhostImport = "github.com/monoes/mono-agent/internal/coderhost"

// queueingNames queue a command on a Store (matched by method name in a file that imports coderhost).
var queueingNames = map[string]bool{"Enqueue": true, "EnqueueSendBounded": true, "EnqueueWithCheckin": true, "EnqueueWithPursuit": true, "enqueueOn": true}

// personNames speak for the person; their callers must be the person-gated verbs.
var personNames = map[string]bool{"TellLeadAboutPlan": true, "AnswerAncestorQuestion": true, "DenyPlan": true, "ProvePerson": true}

// personIdents are the in-package names that mark a command as the person's or build its reserved turn id; they are
// matched as identifiers inside coderhost only.
var personIdents = map[string]bool{"answerFromPerson": true, "askFromPerson": true, "reservedPersonTurn": true, "planCommand": true}

// knownPersonSites is the whole list: "dir/file.go:func:name" -> number of uses. A new entry needs a review of whether
// the caller is behind the person gate (cli/task personGate / RequireOperator) and a line here saying why.
var knownPersonSites = map[string]int{
	// outside coderhost: queueing
	"internal/cli/chat/coder_boss_guard.go:enqueueBounded:EnqueueSendBounded": 1, // a person's chat send (t- turn id, no issuer; reserved prefixes refused by enqueueOn)
	"internal/cli/chat/coder_sessions.go:enqueue:Enqueue":                     1, // the coder send/steer/stop/pause CLI verbs: the person's plain send, same refusal
	// outside coderhost: person helpers, all behind personGate and a ProvePerson of its actor
	"internal/cli/task/session_person.go:newPersonAskCmd:ProvePerson":                      1, // person ask <plan>: personGate -> ProvePerson -> TellLeadAboutPlan
	"internal/cli/task/session_person.go:newPersonAskCmd:TellLeadAboutPlan":                1,
	"internal/cli/task/session_person.go:newPersonDenyCmd:ProvePerson":                     1, // person deny <plan>: personGate -> ProvePerson -> DenyPlan
	"internal/cli/task/session_person.go:newPersonDenyCmd:DenyPlan":                        1,
	"internal/cli/task/session_person_ask.go:newPersonAnswerAskCmd:ProvePerson":            1, // person answer-ask: personGate -> ProvePerson -> AnswerAncestorQuestion
	"internal/cli/task/session_person_ask.go:newPersonAnswerAskCmd:AnswerAncestorQuestion": 1,
	// outside coderhost, not the coder queue: the doomsday governor's own queue, in a file that imports coderhost for its Launcher type
	"internal/cli/doomsday/governorlauncher.go:admit:Enqueue": 1,
	// outside coderhost: the operator CLI's parked-question answer (OperatorScope), behind the operator gate
	"internal/cli/chat/coder_sessions_autonomy.go:newQuestionsAnswerCmd:Answer": 1,
	// names that match but are not the coder queue (reviewed false positives): other Enqueue methods
	"internal/capturesummary/summarizer.go:Handle:Enqueue":             1, // the capture summary queue
	"internal/cli/capturehooks/index.go:handleIndexCapture:Enqueue":    1, // the capture index queue
	"internal/cli/capturehooks/index.go:handleIndexSummary:Enqueue":    1,
	"internal/doomsday/governor/sched/sched.go:Enqueue:Enqueue":        1, // the governor's own queue
	"internal/workflow/engine_loops.go:ResumeExecution:Enqueue":        1, // the workflow execution queue (ExecutionRequest)
	"internal/workflow/engine_loops.go:adoptQueuedExecutions:Enqueue":  1,
	"internal/workflow/engine_run.go:RetryExecution:Enqueue":           1,
	"internal/workflow/engine_run.go:TriggerWorkflow:Enqueue":          1,
	"internal/workflow/engine_run.go:handleTrigger:Enqueue":            1,
	"internal/workflow/engine_subworkflow.go:StartSubWorkflow:Enqueue": 1,
	"internal/workflow/engine_webhook.go:handleWebhookTrigger:Enqueue": 1,
	"internal/workflow/error_workflow.go:fireErrorWorkflow:Enqueue":    1,
	"internal/workflow/evaluation.go:StartEvaluationCase:Enqueue":      1,
	// inside coderhost: every place that queues a command (the older TestEveryCommandQueueingSiteIsKnown holds the
	// reasons per site; this list makes the same sites part of the whole-tree guard)
	"internal/coderhost/ask_ancestor.go:publishAsk:enqueueOn":           1,
	"internal/coderhost/ask_reply.go:closeAndDeliver:enqueueOn":         1,
	"internal/coderhost/checkin.go:checkinOne:EnqueueWithCheckin":       1,
	"internal/coderhost/children.go:spawnWith:Enqueue":                  1,
	"internal/coderhost/children_control.go:ControlChild:Enqueue":       1,
	"internal/coderhost/children_control.go:SendToChild:Enqueue":        1,
	"internal/coderhost/missionchild.go:queueCascadeStop:Enqueue":       1,
	"internal/coderhost/missionchild.go:reconcileChild:Enqueue":         1,
	"internal/coderhost/parked_deliver.go:deliver:Enqueue":              1,
	"internal/coderhost/person_deny.go:tellDenied:Enqueue":              1,
	"internal/coderhost/person_plan.go:TellLeadAboutPlan:Enqueue":       1,
	"internal/coderhost/pursuit_wake.go:EnqueueWithPursuit:enqueueOn":   1,
	"internal/coderhost/pursuit_wake.go:pursuitWake:EnqueueWithPursuit": 1,
	"internal/coderhost/send_bounded.go:EnqueueSendBounded:enqueueOn":   1,
	"internal/coderhost/session_answer.go:AnswerAsMaster:Answer":        1, // the master's answer, under a token scope; the turn id is answer-m-, never reserved
	"internal/coderhost/sessionspawn.go:SpawnSession:Enqueue":           1,
	"internal/coderhost/store.go:Enqueue:enqueueOn":                     1,
	"internal/coderhost/store.go:EnqueueWithCheckin:enqueueOn":          1,
	"internal/coderhost/store.go:INSERT":                                2, // enqueueOn, and enqueueCascadeResume (a resume command, never a send)
	"internal/coderhost/treecascade.go:queueCascade:Enqueue":            1,
	// inside coderhost: where the person's marker and reserved ids are built
	"internal/coderhost/person_plan.go:planCommand:answerFromPerson":         1, // the plan words' turn id, marked person
	"internal/coderhost/person_deny.go:DenyPlan:planCommand":                 1,
	"internal/coderhost/person_deny.go:tellDenied:planCommand":               1,
	"internal/coderhost/person_deny.go:tellDenied:answerFromPerson":          1, // answer-p-deny-<item>-<round>
	"internal/coderhost/person_plan.go:TellLeadAboutPlan:planCommand":        1,
	"internal/coderhost/parked_deliver.go:deliver:answerFromPerson":          1, // the person's parked-question answer (OperatorScope only: answered_by = user)
	"internal/coderhost/ask_reply.go:AnswerAncestorQuestion:askFromPerson":   1, // the person's critical-ask answer, behind the proof
	"internal/coderhost/pursuit_person.go:personActionAt:reservedPersonTurn": 1, // the reader: counts the reserved ids as the person's
	"internal/coderhost/person_proof.go:reservedPersonTurn:answerFromPerson": 1,
	"internal/coderhost/person_proof.go:reservedPersonTurn:askFromPerson":    1,
	"internal/coderhost/store.go:enqueueOn:reservedPersonTurn":               1, // the refusal
	"internal/coderhost/ask_reply.go:closeAndDeliver:askFromPerson":          1, // marks the reply person only for the person's origin; its callers are pinned by the AnswerAncestorQuestion entry
}

// scanPersonSites walks the roots and counts the uses of the watched names per "file:func:name". It returns any walk or
// parse error (a file it cannot read is a file it cannot vouch for) and how many files it parsed.
func scanPersonSites(base string, roots ...string) (map[string]int, int, error) {
	got := map[string]int{}
	visited := 0
	for _, root := range roots {
		err := filepath.WalkDir(filepath.Join(base, root), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			visited++
			rel, _ := filepath.Rel(base, path)
			rel = filepath.ToSlash(rel)
			inside := strings.HasPrefix(rel, "internal/coderhost/")
			dot := false // import . ".../coderhost": calls are bare identifiers
			for _, im := range f.Imports {
				if p, _ := strconv.Unquote(im.Path.Value); p == coderhostImport && im.Name != nil && im.Name.Name == "." {
					dot = true
				}
			}
			for _, decl := range f.Decls {
				fn := ""
				if fd, ok := decl.(*ast.FuncDecl); ok {
					fn = fd.Name.Name
				}
				sel := map[*ast.Ident]bool{}
				ast.Inspect(decl, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.SelectorExpr: // every selector, so a method value or an interface call counts
						sel[x.Sel] = true
						if queueingNames[x.Sel.Name] || personNames[x.Sel.Name] {
							got[rel+":"+fn+":"+x.Sel.Name]++
						}
					case *ast.Ident:
						switch {
						case sel[x]:
						case dot && (queueingNames[x.Name] || personNames[x.Name]):
							got[rel+":"+fn+":"+x.Name]++
						case inside && fn != "" && personIdents[x.Name] && x.Name != fn:
							got[rel+":"+fn+":"+x.Name]++
						}
					case *ast.CallExpr: // Store.Answer(profile, id, text, scope): the parked-question answer, 4 arguments
						name := ""
						switch fx := x.Fun.(type) {
						case *ast.SelectorExpr:
							name = fx.Sel.Name
						case *ast.Ident:
							if dot {
								name = fx.Name
							}
						}
						if name == "Answer" && len(x.Args) == 4 {
							got[rel+":"+fn+":Answer"]++
						}
					case *ast.BasicLit: // a raw INSERT into the queue table, wherever it is
						if x.Kind == token.STRING && insertCommands.MatchString(x.Value) {
							got[rel+":INSERT"]++
						}
					}
					return true
				})
			}
			return nil
		})
		if err != nil {
			return nil, visited, fmt.Errorf("scanning %s: %w", root, err)
		}
	}
	return got, visited, nil
}

func diffPersonSites(got map[string]int, want map[string]int) []string {
	var out []string
	for k, n := range got {
		switch {
		case want[k] == 0:
			out = append(out, "new site "+k+": it queues a command or speaks for the person. Review it against the away-limit rule (wave-s4-sec2/sec3, issue #1548): is it behind the person gate? then list it in knownPersonSites")
		case want[k] != n:
			out = append(out, k+" has a different number of uses than listed: review the new one and update the list")
		}
	}
	for k := range want {
		if got[k] == 0 {
			out = append(out, "listed site "+k+" is gone: update the list")
		}
	}
	sort.Strings(out)
	return out
}

func TestEveryPersonQueueingSiteIsKnown(t *testing.T) {
	got, visited, err := scanPersonSites("../..", "internal", "cmd")
	if err != nil {
		t.Fatal(err)
	}
	if visited < 500 {
		t.Fatalf("the guard parsed only %d files of internal/ and cmd/: it would pass on nothing", visited)
	}
	for _, k := range []string{"internal/cli/chat/coder_sessions.go:enqueue:Enqueue", "internal/cli/task/session_person_ask.go:newPersonAnswerAskCmd:AnswerAncestorQuestion"} {
		if got[k] == 0 {
			t.Fatalf("the guard does not see %s: it is not looking where it should", k)
		}
	}
	for _, msg := range diffPersonSites(got, knownPersonSites) {
		t.Error(msg)
	}
}

// A caller the guard does not know, planted in a package of its own, is reported, whatever name its file has.
func TestAPlantedPersonSiteTripsTheGuard(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "internal", "mcp", "evil")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `package evil

import "` + coderhostImport + `"

func Reply(cs *coderhost.Store) {
	_, _ = cs.TellLeadAboutPlan(nil, coderhost.PersonProof{}, "p", "c", 1, "asked", "x")
	_, _, _ = cs.AnswerAncestorQuestion(coderhost.PersonProof{}, "p", 1, "x")
	_, _ = cs.Enqueue(coderhost.Command{})
}
`
	if err := os.WriteFile(filepath.Join(dir, "evil.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got, visited, err := scanPersonSites(base, "internal", "cmd")
	if err != nil {
		t.Fatal(err)
	}
	if visited != 1 {
		t.Fatalf("visited %d files, want 1", visited)
	}
	msgs := diffPersonSites(got, nil)
	for _, name := range []string{"TellLeadAboutPlan", "AnswerAncestorQuestion", "Enqueue"} {
		found := false
		for _, m := range msgs {
			found = found || strings.Contains(m, "evil.go:Reply:"+name)
		}
		if !found {
			t.Errorf("the planted %s call was not reported: %v", name, msgs)
		}
	}
}

func TestThePersonSiteScanDoesNotHideAParseError(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "cmd", "broken.go"), []byte("package x\nfunc ("), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := scanPersonSites(base, "cmd"); err == nil {
		t.Fatal("a file that does not parse was skipped")
	}
	if _, _, err := scanPersonSites(base, "missing"); err == nil {
		t.Fatal("a root that does not exist was skipped")
	}
}

func plant(t *testing.T, src string) map[string]int {
	t.Helper()
	base := t.TempDir()
	for _, d := range []string{"cmd", "internal/mcp/evil"} {
		if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "internal", "mcp", "evil", "evil.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _, err := scanPersonSites(base, "internal", "cmd")
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// The shapes a plain name-and-import scan would miss.
func TestThePersonSiteScanSeesTheShapesThatSlipPastAnImportFilter(t *testing.T) {
	imp := `import "` + coderhostImport + `"`
	for name, c := range map[string]struct{ src, key string }{
		"raw insert":                   {"package evil\n\nconst q = `INSERT INTO coder_session_commands (x) VALUES (1)`\n", "evil.go:INSERT"},
		"interface without the import": {"package evil\n\ntype Q interface{ Enqueue(x any) }\n\nfunc F(q Q) { q.Enqueue(1) }\n", "evil.go:F:Enqueue"},
		"dot import":                   {"package evil\n\nimport . \"" + coderhostImport + "\"\n\nfunc F() { _, _ = TellLeadAboutPlan(nil, PersonProof{}, \"p\", \"c\", 1, \"a\", \"x\") }\n", "evil.go:F:TellLeadAboutPlan"},
		"answer":                       {"package evil\n\n" + imp + "\n\nfunc F(s *coderhost.Store) { _, _, _ = s.Answer(\"p\", 1, \"x\", coderhost.OperatorScope{}) }\n", "evil.go:F:Answer"},
	} {
		found := false
		for k := range plant(t, c.src) {
			found = found || strings.HasSuffix(k, c.key)
		}
		if !found {
			t.Errorf("%s: the planted site was not seen", name)
		}
	}
}

func TestNewQueueingCallersInsideCoderhostAreListed(t *testing.T) {
	for _, k := range []string{"internal/coderhost/children_control.go:SendToChild:Enqueue", "internal/coderhost/store.go:INSERT"} {
		if knownPersonSites[k] == 0 {
			t.Errorf("%s is not listed: the guard does not cover the queueing callers inside coderhost", k)
		}
	}
}
