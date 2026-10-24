package extension

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
)

// coder.task: the side panel's "Fill with coder" and "Ask coder" (issue #958).
// It queues an AI task (internal/aitask kinds extension-fill and extension-ask)
// about one open tab and starts nothing: like coder.queue, the person presses
// Start in the desktop app, so a page, or a panel acting for it, can never
// begin a full-access session (decision 3 of 2026-10-15). Everything is
// authorised the way coder.queue is: the account door (serveRequest) refuses
// it while signed out, the profile must be named, and the CLI is this binary
// without the bridge's agent markers, so `aitask submit-page` sees the person.
// The note travels on stdin; argv holds only the kind, ids and numbers.
//
//	coder.task {kind, tabId, note?, profile?} -> {task}
//	           kind: extension-fill | extension-ask (ask needs a note)
//	           refused with code coder_disabled when coder mode is off: the
//	           caller falls back to the quick form.fill / doc.ask.
const MethodCoderTask = "coder.task"

// coderTaskKinds are the aitask kinds coder.task may create. They are
// repeated here, not imported, because the bridge never opens the task store.
var coderTaskKinds = []string{"extension-fill", "extension-ask"}

func (cb *coderBridge) handleTask(ctx context.Context, req *Request, _ ProgressFunc) (any, error) {
	profile, err := coderProfile(req)
	if err != nil {
		return nil, err
	}
	kind := req.String("kind")
	if !slices.Contains(coderTaskKinds, kind) {
		return nil, invalidParam("kind must be one of %s", strings.Join(coderTaskKinds, ", "))
	}
	tab := req.Int("tabId", 0)
	if tab <= 0 {
		return nil, invalidParam("coder.task needs a tabId")
	}
	note := req.String("note")
	if len(note) > coderMaxNote {
		return nil, invalidParam("note too long")
	}
	if kind == "extension-ask" && strings.TrimSpace(note) == "" {
		return nil, invalidParam("extension-ask needs a question")
	}
	args := append(chatBase(profile), "aitask", "submit-page", "--kind="+kind, "--tab="+strconv.Itoa(tab), "--stdin-json")
	if inst := req.Origin.Instance; chatIDPattern.MatchString(inst) {
		args = append(args, "--browser="+inst)
	}
	stdin, _ := json.Marshal(map[string]any{"note": note})
	task, err := cb.runJSON(ctx, string(stdin), args...)
	if err != nil {
		return nil, err
	}
	return map[string]any{"task": task}, nil
}
