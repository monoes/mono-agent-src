package extension

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// coder.*: the side panel's half of "the browser hands work to the coder"
// (C5 of docs/mastermind/plans/2026-10-15-coder-browser.md). The extension
// never starts a full-access turn: coder.queue only queues one, and the
// desktop app starts it on the person's click. Follow-ups (coder.send) go
// only to a conversation whose first turn was started in the desktop app.
// Like chat.*, the bridge never opens the database: every method runs this
// binary's own CLI, page text and the person's text travel on stdin, and
// argv carries only ids, numbers and a folder the person picked.
//
//	coder.queue      {kind, tabId, selector?, note?, cwd?, reload?, profile?} -> {item}
//	                 reload (bug-report): capture console and network across a reload
//	coder.task       {kind, tabId, note?, profile?} -> {task}   (coder_task.go: a queued AI task)
//	coder.queue.list {profile?}                                     -> {items}
//	coder.projects   {profile?}                                     -> {projects, recent}
//	coder.list       {profile?}                                     -> {conversations}
//	coder.events     {conversationId, since?, turn?, profile?}
//	                 -> {turn, events, turn_active, images:[{seq,path,dataUrl}]}
//	coder.send       {conversationId, text, profile?}               -> {conversationId, turnId}
//	coder.stop       {conversationId}                               -> {stopped}

// Coder request methods.
const (
	MethodCoderQueue     = "coder.queue"
	MethodCoderQueueList = "coder.queue.list"
	MethodCoderProjects  = "coder.projects"
	MethodCoderList      = "coder.list"
	MethodCoderEvents    = "coder.events"
	MethodCoderSend      = "coder.send"
	MethodCoderStop      = "coder.stop"
)

// CodeNotStartedInDesktop refuses a follow-up to a conversation the person
// never started in the desktop app.
const CodeNotStartedInDesktop = "not_started_in_desktop"

const (
	coderQueueTimeout = 3 * time.Minute
	coderSendTimeout  = time.Minute
	coderMaxPath      = 4096
	coderMaxNote      = 8 * 1024
	coderMaxSelector  = 1024
)

var coderKinds = []string{"copy-style", "tokens", "fix-element", "bug-report", "integration", "ask"}

// coderCLI runs this binary's CLI. Tests replace it with a fake.
type coderCLI interface {
	// Run runs args to completion with stdin and returns stdout.
	Run(ctx context.Context, stdin string, args ...string) ([]byte, error)
	// Start starts a turn process; it is killed when ctx is cancelled.
	Start(ctx context.Context, stdin string, args ...string) (coderProc, error)
}

// coderProc is a started turn process.
type coderProc interface {
	Stdout() io.Reader
	Wait() error
}

type coderBridge struct {
	cli      coderCLI
	instance string

	mu     sync.Mutex
	active map[string]context.CancelFunc // profile+conversation -> stop, for turns this bridge started
	cwds   map[string]string             // profile+conversation -> its folder (fixed for its life)
}

// registerCoderHandlers installs coder.* on every server, run through this
// binary's CLI (which refuses unless the process is monoagentcli).
func registerCoderHandlers(s *Server) { registerCoderBridge(s, &cliCoder{}) }

func registerCoderBridge(s *Server, cli coderCLI) *coderBridge {
	cb := &coderBridge{cli: cli, instance: "ext-" + randHex(6), active: map[string]context.CancelFunc{}, cwds: map[string]string{}}
	s.HandleRequestWithTimeout(MethodCoderQueue, cb.handleQueue, coderQueueTimeout)
	s.HandleRequestWithTimeout(MethodCoderTask, cb.handleTask, coderQueueTimeout)
	s.HandleRequestWithTimeout(MethodCoderQueueList, cb.passThrough("coder", "queue", "list"), chatQueryTimeout)
	s.HandleRequestWithTimeout(MethodCoderProjects, cb.passThrough("coder", "projects", "list"), chatQueryTimeout)
	s.HandleRequestWithTimeout(MethodCoderList, cb.passThrough("coder", "list"), chatQueryTimeout)
	s.HandleRequestWithTimeout(MethodCoderEvents, cb.handleEvents, chatQueryTimeout)
	s.HandleRequestWithTimeout(MethodCoderSend, cb.handleSend, coderSendTimeout)
	s.HandleRequest(MethodCoderStop, cb.handleStop)
	return cb
}

// coderProfile is the profile param, else the asking browser's. A request
// that names neither is refused, as the chat methods refuse it.
func coderProfile(req *Request) (string, error) {
	p, err := chatProfile(req)
	if err != nil || p != "" {
		return p, err
	}
	if o := req.Origin.Profile; o != "" && safeArgValue(o) && !strings.Contains(o, "=") {
		return o, nil
	}
	return "", invalidParam("%s", chooseProfileMessage)
}

func coderConversationParam(req *Request) (string, error) {
	c := req.String("conversationId")
	if !chatIDPattern.MatchString(c) {
		return "", invalidParam("invalid conversationId")
	}
	return c, nil
}

// runJSON runs the CLI and decodes its JSON object.
func (cb *coderBridge) runJSON(ctx context.Context, stdin string, args ...string) (map[string]any, error) {
	out, err := cb.cli.Run(ctx, stdin, args...)
	if err != nil {
		return nil, err
	}
	var v map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out), &v); err != nil {
		return nil, errors.New("monoagentcli returned unreadable output")
	}
	return v, nil
}

// passThrough answers with a read-only command's JSON as it is.
func (cb *coderBridge) passThrough(cmd ...string) RequestHandler {
	return func(ctx context.Context, req *Request, _ ProgressFunc) (any, error) {
		profile, err := coderProfile(req)
		if err != nil {
			return nil, err
		}
		return cb.runJSON(ctx, "", append(chatBase(profile), cmd...)...)
	}
}

func (cb *coderBridge) handleQueue(ctx context.Context, req *Request, _ ProgressFunc) (any, error) {
	profile, err := coderProfile(req)
	if err != nil {
		return nil, err
	}
	kind := req.String("kind")
	if !slices.Contains(coderKinds, kind) {
		return nil, invalidParam("kind must be one of %s", strings.Join(coderKinds, ", "))
	}
	tab := req.Int("tabId", 0)
	if tab <= 0 {
		return nil, invalidParam("coder.queue needs a tabId")
	}
	selector, note, cwd := req.String("selector"), req.String("note"), req.String("cwd")
	if len(selector) > coderMaxSelector || len(note) > coderMaxNote {
		return nil, invalidParam("selector or note too long")
	}
	args := append(chatBase(profile), "coder", "queue", "add", "--kind="+kind, "--tab="+strconv.Itoa(tab), "--stdin-json")
	if inst := req.Origin.Instance; chatIDPattern.MatchString(inst) {
		args = append(args, "--browser="+inst)
	}
	if cwd != "" {
		if !filepath.IsAbs(cwd) || len(cwd) > coderMaxPath || !safeArgValue(cwd) {
			return nil, invalidParam("cwd must be an absolute folder path")
		}
		args = append(args, "--cwd="+cwd)
	}
	reload, _ := req.Params["reload"].(bool)
	stdin, _ := json.Marshal(map[string]any{"selector": selector, "note": note, "reload": reload})
	item, err := cb.runJSON(ctx, string(stdin), args...)
	if err != nil {
		return nil, err
	}
	return map[string]any{"item": item}, nil
}

// coderChat is one row of `coder list --conversation`.
type coderChat struct {
	ID               string `json:"id"`
	Cwd              string `json:"cwd"`
	StartedInDesktop bool   `json:"startedInDesktop"`
	Running          bool   `json:"running"`
}

func (cb *coderBridge) chatRow(ctx context.Context, profile, conv string) (coderChat, error) {
	out, err := cb.cli.Run(ctx, "", append(chatBase(profile), "coder", "list", "--conversation="+conv)...)
	if err != nil {
		return coderChat{}, err
	}
	var res struct{ Conversations []coderChat }
	if json.Unmarshal(out, &res) != nil || len(res.Conversations) != 1 {
		return coderChat{}, errors.New("monoagentcli coder list returned unreadable output")
	}
	row := res.Conversations[0]
	cb.mu.Lock()
	cb.cwds[convKey(profile, conv)] = row.Cwd
	cb.mu.Unlock()
	return row, nil
}

func (cb *coderBridge) handleSend(ctx context.Context, req *Request, _ ProgressFunc) (any, error) {
	profile, err := coderProfile(req)
	if err != nil {
		return nil, err
	}
	conv, err := coderConversationParam(req)
	if err != nil {
		return nil, err
	}
	text := req.String("text")
	if text == "" || len(text) > chatMaxMessage {
		return nil, invalidParam("text must be 1 to %d bytes", chatMaxMessage)
	}
	row, err := cb.chatRow(ctx, profile, conv)
	if err != nil {
		return nil, err
	}
	if !row.StartedInDesktop {
		return nil, &RequestError{Code: CodeNotStartedInDesktop,
			Err: errors.New("start this coder chat in the MonoAgent app first: the browser can only follow up on a chat you started there")}
	}
	if row.Running {
		return nil, errBusy()
	}
	turnCtx, stop := context.WithCancel(context.Background())
	if !cb.claim(profile, conv, stop) {
		stop()
		return nil, errBusy()
	}
	turn := "ext-t-" + randHex(8)
	proc, err := cb.cli.Start(turnCtx, text, append(chatBase(profile), "chat", "--conversation="+conv, "--turn="+turn,
		"--instance="+cb.instance, "--prompt-stdin")...)
	if err != nil {
		cb.release(profile, conv)
		stop()
		return nil, err
	}
	sc := bufio.NewScanner(proc.Stdout())
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	admitted := make(chan []byte, 1)
	go func() {
		var first []byte
		for sc.Scan() {
			if line := sc.Bytes(); len(line) > 0 && line[0] == '{' {
				first = append([]byte(nil), line...)
				break
			}
		}
		admitted <- first
	}()
	var first []byte
	select {
	case first = <-admitted:
	case <-ctx.Done():
		stop() // too slow to start: kill it, and record it stopped if it got in
		first = <-admitted
	}
	var adm struct {
		Existed bool `json:"existed"`
		Turn    struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if json.Unmarshal(first, &adm) == nil && adm.Turn.ID != "" && !adm.Existed {
		go cb.follow(turnCtx, stop, sc, proc, profile, conv, turn)
		if ctx.Err() != nil {
			return nil, &RequestError{Code: CodeTimeout, Err: ctx.Err()}
		}
		return map[string]any{"conversationId": conv, "turnId": turn}, nil
	}
	// Refused (the first line is {"error","code"}): the process exits on
	// its own.
	waitErr := proc.Wait()
	stop()
	cb.release(profile, conv)
	if ctx.Err() != nil {
		return nil, &RequestError{Code: CodeTimeout, Err: ctx.Err()}
	}
	return nil, cliFailure(first, "", orExit(waitErr))
}

// follow drains a running turn's events to its end, and records the end
// itself when the process died without writing turn.finished.
func (cb *coderBridge) follow(ctx context.Context, stop context.CancelFunc, sc *bufio.Scanner, proc coderProc, profile, conv, turn string) {
	defer stop()
	defer cb.release(profile, conv)
	finished := false
	for sc.Scan() {
		var rec chatevents.Record
		if json.Unmarshal(sc.Bytes(), &rec) == nil && rec.Type == chatevents.EventTurnFinished {
			finished = true
		}
	}
	waitErr := proc.Wait()
	if finished {
		return
	}
	st, reason := chatevents.StatusFailed, "monoagentcli ended without finishing the turn"
	if ctx.Err() != nil {
		st, reason = chatevents.StatusCancelled, "stopped from the side panel"
	} else if waitErr != nil {
		reason = "monoagentcli exited: " + waitErr.Error()
	}
	fctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, _ = cb.cli.Run(fctx, "", append(chatBase(profile), "chat", "history", "finish",
		"--status="+string(st), "--reason="+reason, "--", conv, turn)...)
}

// convKey scopes a conversation id to its profile: ids are only unique
// within one, and neither a stop nor a cached folder may cross profiles.
func convKey(profile, conv string) string { return profile + "\x00" + conv }

func (cb *coderBridge) claim(profile, conv string, stop context.CancelFunc) bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	k := convKey(profile, conv)
	if _, busy := cb.active[k]; busy {
		return false
	}
	cb.active[k] = stop
	return true
}

func (cb *coderBridge) release(profile, conv string) {
	cb.mu.Lock()
	delete(cb.active, convKey(profile, conv))
	cb.mu.Unlock()
}

func (cb *coderBridge) handleStop(_ context.Context, req *Request, _ ProgressFunc) (any, error) {
	profile, err := coderProfile(req)
	if err != nil {
		return nil, err
	}
	conv, err := coderConversationParam(req)
	if err != nil {
		return nil, err
	}
	cb.mu.Lock()
	stop := cb.active[convKey(profile, conv)]
	cb.mu.Unlock()
	if stop != nil {
		stop()
	}
	return map[string]any{"stopped": stop != nil}, nil
}

func (cb *coderBridge) handleEvents(ctx context.Context, req *Request, _ ProgressFunc) (any, error) {
	profile, err := coderProfile(req)
	if err != nil {
		return nil, err
	}
	conv, err := coderConversationParam(req)
	if err != nil {
		return nil, err
	}
	turn := req.String("turn")
	if turn != "" && !chatIDPattern.MatchString(turn) {
		return nil, invalidParam("invalid turn id")
	}
	since := req.Int("since", 0)
	if since < 0 {
		return nil, invalidParam("since must not be negative")
	}
	cb.mu.Lock()
	cwd, known := cb.cwds[convKey(profile, conv)]
	cb.mu.Unlock()
	if !known {
		// Also refuses a conversation that is not a coder one.
		row, err := cb.chatRow(ctx, profile, conv)
		if err != nil {
			return nil, err
		}
		cwd = row.Cwd
	}
	page, err := cb.listEvents(ctx, profile, conv, turn, int64(since))
	if err != nil {
		return nil, err
	}
	return map[string]any{"turn": page.Turn, "events": page.Events, "turn_active": page.TurnActive,
		"images": coderImages(cwd, page.Events)}, nil
}

// listEvents is chat.events over coderCLI: turn's events after since, or
// the latest turn's when turn is empty.
func (cb *coderBridge) listEvents(ctx context.Context, profile, conv, turn string, since int64) (ChatEventsPage, error) {
	if turn == "" {
		out, err := cb.cli.Run(ctx, "", append(chatBase(profile), "chat", "history", "turns", "--limit=1", "--", conv)...)
		if err != nil {
			return ChatEventsPage{}, err
		}
		var turns struct{ Items []struct{ ID string } }
		if json.Unmarshal(out, &turns) != nil {
			return ChatEventsPage{}, errors.New("chat history turns returned unreadable output")
		}
		if len(turns.Items) == 0 {
			return ChatEventsPage{Events: []ChatEvent{}}, nil
		}
		turn = turns.Items[0].ID
	}
	out, err := cb.cli.Run(ctx, "", append(chatBase(profile), "chat", "history", "events",
		"--after-seq="+strconv.FormatInt(since, 10), "--", conv, turn)...)
	if err != nil {
		return ChatEventsPage{}, err
	}
	var page struct {
		Items []chatevents.Record `json:"items"`
		Turn  struct{ Status string }
	}
	if json.Unmarshal(out, &page) != nil {
		return ChatEventsPage{}, errors.New("chat history events returned unreadable output")
	}
	res := ChatEventsPage{Turn: turn, Events: make([]ChatEvent, 0, len(page.Items)), TurnActive: page.Turn.Status == "active"}
	for _, r := range page.Items {
		res.Events = append(res.Events, ChatEvent{Seq: r.Seq, Type: string(r.Type), Payload: r.Payload})
	}
	return res, nil
}
