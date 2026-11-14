package chat

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The tree's non-test Go files under internal, cmd and wails-app, parsed once per test.
func treeFiles(t *testing.T) map[string]string {
	t.Helper()
	root := filepath.Join("..", "..", "..")
	out := map[string]string{}
	for _, dir := range []string{"internal", "cmd", "wails-app"} {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			out[filepath.ToSlash(rel)] = string(b)
			return nil
		})
	}
	return out
}

func filesWith(files map[string]string, needle string) []string {
	var got []string
	for f, src := range files {
		if strings.Contains(src, needle) {
			got = append(got, f)
		}
	}
	sort.Strings(got)
	return got
}

// Every place that can start a turn in a conversation, and what keeps it from starting one in a boss's. A new file that names
// one of these fails here until it is added with its refusal (or its reason it cannot reach a boss).
//
//   - "--turn" is how a process asks `chat` to run a journaled turn: every one of them ends in the `chat --conversation` guard
//     (refuseBossTurnWithoutHostIdentity), which wants the session token the host minted for that turn.
//   - KindSend is a queued send: the entries that a person or a program can reach are `coder sessions send` (refuses a boss's
//     conversation), the steer and the first turn (the user's own, bounded), the extension (refuses a boss by name and goes
//     through `chat`), and the host's own channels (ask, reply, parked, plan, check-in, children, spawn): their rows run only as the
//     host launches them, which is the identity the guard wants.
var turnEntryTable = map[string]map[string]string{
	"--turn": {
		"internal/cli/aitask/launcher.go":      "an AI task's turn: `chat --conversation`, so the guard (a boss's conversation is not an AI task's)",
		"internal/cli/chat/coder_host_exec.go": "the host's own launch: it mints the session token the guard wants",
		"internal/extension/chat_cli.go":       "the extension's scoped chat turn: `chat --conversation`, so the guard",
		"internal/extension/coder_bridge.go":   "the extension's coder send: refuses a boss by name, then `chat --conversation`, so the guard",
		"wails-app/app_chat_host.go":           "the app's host send: `coder sessions send`, which refuses a boss's conversation",
		"wails-app/app_chat_turns.go":          "the app's scoped turn: `chat --conversation`, so the guard",
		"wails-app/app_coder.go":               "the app's coder turn: `chat --conversation` / `coder sessions send`, so the guard and the refusal",
	},
	"KindSend": {
		"internal/cli/chat/coder_boss_first.go":     "the boss's first turn: the user's own, bounded, for a boss of a live run",
		"internal/cli/chat/coder_sessions.go":       "`coder sessions send`: refuses a boss's conversation",
		"internal/cli/chat/coder_sessions_steer.go": "the steer: the user's own, bounded, wrapped as untrusted guidance",
		"internal/coderhost/ask_ancestor.go":        "an ask to a lead: a host channel, run only as the host launches it",
		"internal/coderhost/ask_reply.go":           "the reply to an ask: a host channel",
		"internal/coderhost/checkin.go":             "a check-in: a host channel",
		"internal/coderhost/children_control.go":    "a master's send to its child: a host channel (a master's descendants only)",
		"internal/coderhost/children.go":            "a master's first message to a new child: a host channel",
		"internal/coderhost/concurrency.go":         "the host's per-chat concurrency",
		"internal/coderhost/host.go":                "the host loop that launches the turns",
		"internal/coderhost/missionconfirm.go":      "the mission confirmation: a send marker, not a producer",
		"internal/coderhost/parked_deliver.go":      "an answer to a parked question: a host channel",
		"internal/coderhost/person_plan.go":         "the person's words about a plan: a host channel (the user's CLI verbs)",
		"internal/coderhost/pursuit_check_wake.go":  "the accept wake: a host channel like the pursuit's wake-up (pursuit-accept- turn id, no issuer), queued only for the lead of a mission whose pursuit the person started, after the person's own answers accepted a check, and runs only as the host launches it, after its run state, away limits and the accepted check are read again",
		"internal/coderhost/pursuit_wake.go":        "the pursuit loop's wake-up: a host channel like a check-in (pursuit- turn id, no issuer), queued only for the lead of a mission whose pursuit the person started and runs only as the host launches it, after its run state and away limits are read again",
		"internal/coderhost/sessionspawn.go":        "a lead's first message to a spawned session: a host channel",
		"internal/coderhost/send_bounded.go":        "the bounded insert: it refuses anything but a person's send",
		"internal/coderhost/store.go":               "the queue itself",
		"internal/doomsday/ops/approval.go":         "the open-ended run's approvals (a different Kind)",
		"internal/doomsday/ops/types.go":            "the open-ended run's operation kinds (a different Kind)",
	},
	"EnqueueSendBounded": {
		"internal/cli/chat/coder_boss_guard.go": "enqueueBounded: the one caller, for the steer and the first turn",
		"internal/coderhost/send_bounded.go":    "the bounded insert itself (person sends only)",
	},
}

func TestEveryEntryThatCanStartATurnIsInTheTable(t *testing.T) {
	files := treeFiles(t)
	for needle, table := range turnEntryTable {
		quoted := needle
		if needle == "--turn" {
			quoted = `"--turn`
		}
		got := filesWith(files, quoted)
		var want []string
		for f := range table {
			want = append(want, f)
		}
		sort.Strings(want)
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s: the files that name it changed.\nnow:  %v\ntable: %v\nAdd the new one to turnEntryTable with the refusal that keeps it out of a boss's conversation.", needle, got, want)
		}
	}
}

// Who may call the bounded send: only enqueueBounded calls EnqueueSendBounded, and enqueueBounded is called only by the steer and
// the first turn, each of which asks requireUser (the user's own gate) in the same function.
func TestBoundedSendIsCalledOnlyByTheUsersOwnVerbs(t *testing.T) {
	files := treeFiles(t)
	fset := token.NewFileSet()
	callers := map[string]map[string]bool{"EnqueueSendBounded": {}, "enqueueBounded": {}}
	asksUser := map[string]bool{}
	for name, src := range files {
		if !strings.Contains(src, "EnqueueSendBounded") && !strings.Contains(src, "enqueueBounded") {
			continue
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			has := map[string]bool{}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fn := call.Fun.(type) {
				case *ast.SelectorExpr:
					has[fn.Sel.Name] = true
				case *ast.Ident:
					has[fn.Name] = true
				}
				return true
			})
			for _, fnName := range []string{"EnqueueSendBounded", "enqueueBounded"} {
				if has[fnName] && !(fd.Name.Name == "enqueueBounded" && fnName == "enqueueBounded") && name != "internal/coderhost/send_bounded.go" {
					callers[fnName][name+":"+fd.Name.Name] = true
					if has["requireUser"] {
						asksUser[name+":"+fd.Name.Name] = true
					}
				}
			}
		}
	}
	if got := keys(callers["EnqueueSendBounded"]); strings.Join(got, ",") != "internal/cli/chat/coder_boss_guard.go:enqueueBounded" {
		t.Errorf("EnqueueSendBounded is called by %v", got)
	}
	want := []string{"internal/cli/chat/coder_boss_first.go:QueueBossFirstTurn", "internal/cli/chat/coder_sessions_steer.go:newCoderSessionsSteerCmd"}
	if got := keys(callers["enqueueBounded"]); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("enqueueBounded is called by %v, want %v", got, want)
	}
	for _, c := range want {
		if !asksUser[c] {
			t.Errorf("%s does not ask requireUser", c)
		}
	}
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// There is ONE definition of "a session the operator may record as the run's boss": a SQL string that names both the unstamped
// charter and the mission kind exists exactly once in the tree (doomsday.BossCandidateSQL). A second copy (an entry screen's own
// check) fails here until it uses the exported one.
func TestBossEligibilityHasOneDefinition(t *testing.T) {
	files := treeFiles(t)
	fset := token.NewFileSet()
	var found []string
	for name, src := range files {
		if !strings.Contains(src, "doomsday_charter_id") {
			continue
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				v := strings.Join(strings.Fields(lit.Value), " ")
				if strings.Contains(v, "doomsday_charter_id = ''") && strings.Contains(v, "kind = 'mission'") {
					found = append(found, name)
				}
			}
			return true
		})
	}
	sort.Strings(found)
	if strings.Join(found, ",") != "internal/cli/doomsday/bossbind.go" {
		t.Errorf("boss eligibility is defined in %v: use doomsday.BossCandidateSQL, the one definition", found)
	}
}
