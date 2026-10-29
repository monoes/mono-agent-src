package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
)

// DeleteKept says why a chat was not deleted although nothing went wrong: the CLI declined, and what it
// says is for the person to act on. Code is "survivors_remain" (deleting stops the processes the chat's
// turns left running, and some could not be stopped: they are listed in Survivors, and the chat is kept
// until they are) or "operator_only" (deleting this chat stops processes or a mission, which only the
// operator may do).
type DeleteKept struct {
	Code      string     `json:"code"`
	Message   string     `json:"message"`
	Stopped   []int      `json:"stopped"`
	Survivors []Survivor `json:"survivors"`
}

// deleteAnswer is what one `chat history delete` came to: it deleted, it declined (Kept), or it failed (Err).
type deleteAnswer struct {
	Deleted bool
	Kept    *DeleteKept
	Err     error
}

const (
	keptSurvivorsRemain = "survivors_remain"
	keptOperatorOnly    = "operator_only"
)

// historyDelete runs `monoagentcli --profile P --json chat history delete <id>` and keeps what the
// plain call loses: at exit 5 the survivors printed before the error document, and the code of a
// refusal. Any other failure is an *aitaskError as with historyCLI, so callers that tell an active turn
// by its message keep working.
func (a *App) historyDelete(id string) deleteAnswer {
	full, err := a.scopedArgs("chat", "history", "delete", id)
	if err != nil {
		return deleteAnswer{Err: err}
	}
	find := findMonoAgentCLI
	if a.chatSup != nil && a.chatSup.findCLI != nil {
		find = a.chatSup.findCLI // the supervisor's own finder, as the other chat calls use
	}
	bin, err := find()
	if err != nil {
		return deleteAnswer{Err: err}
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, bin, full...)
	suppressConsole(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err == nil {
		return deleteAnswer{Deleted: true}
	}
	var ref aitaskRefusal
	_ = json.Unmarshal([]byte(lastLine(string(out))), &ref)
	var ee *exec.ExitError
	exit := 0
	if errors.As(err, &ee) {
		exit = ee.ExitCode()
	}
	switch {
	case exit == exitSurvivorsRemain:
		// The first JSON document is the result: {"id","deleted":false,"stopped":[…],"survivors":[…]}.
		var res struct {
			Stopped   []int      `json:"stopped"`
			Survivors []Survivor `json:"survivors"`
		}
		_ = json.NewDecoder(bytes.NewReader(out)).Decode(&res)
		kept := &DeleteKept{Code: keptSurvivorsRemain, Message: ref.Error, Stopped: res.Stopped, Survivors: res.Survivors}
		if kept.Stopped == nil {
			kept.Stopped = []int{}
		}
		if kept.Survivors == nil {
			kept.Survivors = []Survivor{}
		}
		return deleteAnswer{Kept: kept}
	case ref.Code == keptOperatorOnly:
		return deleteAnswer{Kept: &DeleteKept{Code: keptOperatorOnly, Message: ref.Error, Stopped: []int{}, Survivors: []Survivor{}}}
	}
	msg := ref.Error
	if msg == "" {
		msg = cliStderr(stderr.Bytes())
	}
	if msg == "" {
		msg = err.Error()
	}
	return deleteAnswer{Err: &aitaskError{msg: msg, code: ref.Code, fallback: ref.Fallback}}
}
