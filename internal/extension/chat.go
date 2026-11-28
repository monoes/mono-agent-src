package extension

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/a2ui"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/capturesummary"
	"github.com/monoes/mono-agent/internal/profiledir"
)

// chat.send / chat.stop / chat.events: the side panel's chat, driven over the
// request channel. The bridge never opens the database or talks to a model
// itself: a ChatBackend (cmd/monoagentcli installs one that runs this
// binary's own `chat --conversation … --turn …`, the same child process the
// desktop app's supervisor runs) does the work, so the panel and the app can
// never disagree about what a turn is.
//
//	chat.send   {profile?, conversation?, runtime?, model?, message,
//	             context?:{url,title,text,selection},
//	             images?:[{mime,data(base64)}]}
//	  progress  stage = chatevents type, detail = {"seq":N,"payload":{...}}
//	  reply     {conversation, turn, text}
//	chat.stop   {conversation}            -> {stopped: bool}   (ok when idle)
//	chat.events {profile?, conversation, after_seq?, turn?}
//	            -> {turn, events:[{seq,type,payload}], turn_active}
//	chat.ui_action {profile, conversation, action}   (chat_ui.go)
//	chat.ui_sync   {profile, conversation}
//	chat.ui_journal {profile, conversation, seen?}
//
// The page context is text from a web page: it is fenced as untrusted data in
// front of the message (chat_context.go), and every turn gets the page-read
// tool set ("monoagent:read": three read-only workflow tools, no secrets,
// vault, messages, people or writes, no runs, no shell).

// Chat request methods.
const (
	MethodChatSend   = "chat.send"
	MethodChatStop   = "chat.stop"
	MethodChatEvents = "chat.events"
)

const (
	chatSendTimeout  = 10 * time.Minute
	chatQueryTimeout = 30 * time.Second
	chatMaxMessage   = a2ui.MaxNextTurnBytes
	chatMaxCtxTitle  = 512
	chatMaxCtxText   = 24 * 1024
	chatMaxCtxSel    = 8 * 1024

	chatMaxCtxTranscript = 24 * 1024
	chatMaxCtxDesc       = 4 * 1024
	chatMaxCtxChannel    = 256
	chatMaxModelBytes    = 128

	// Pictures for the model: a screenshot of the tab, or images the person
	// attached. The panel shrinks them to fit; these are the bridge's own limits.
	chatMaxImages     = 3
	chatMaxImageBytes = 2 << 20
)

// chatIDPattern is what a conversation or turn id must look like before it
// can become an argv word (same rule as the CLI's own control ids).
var chatIDPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)

// ChatTurnSpec is one turn to run, always in the page-read tool mode.
type ChatTurnSpec struct {
	Profile      string
	Conversation string
	Turn         string
	Instance     string
	Message      string
	// Attach are image files written for this turn (0600, in a folder that goes
	// when the turn ends); the runner gets them as `agent exec --attach`.
	Attach []string
}

// ChatEvent is one journaled event of a turn.
type ChatEvent struct {
	Seq     int64           `json:"seq"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
	// At is when it was journaled (RFC 3339); the panel orders cards of different turns by it.
	At string `json:"at,omitempty"`
}

// ChatEventsPage is what ListEvents returns.
type ChatEventsPage struct {
	Turn       string      `json:"turn"`
	Events     []ChatEvent `json:"events"`
	TurnActive bool        `json:"turn_active"`
}

// ChatBackend runs chat turns. An error that must reach the extension with a
// code is a *RequestError (CodeBusy, CodeInvalid, CodeUnavailable).
type ChatBackend interface {
	// CreateConversation makes an agent conversation and returns its id.
	CreateConversation(ctx context.Context, profile, runtime, model string) (string, error)
	// RunTurn runs spec to its end, calling onEvent for each event in order.
	// Cancelling ctx stops the turn (and the backend records it cancelled).
	RunTurn(ctx context.Context, spec ChatTurnSpec, onEvent func(ChatEvent)) error
	// ListEvents returns a conversation's events after afterSeq: those of
	// turn, or of its latest turn when turn is empty.
	ListEvents(ctx context.Context, profile, conversation, turn string, afterSeq int64) (ChatEventsPage, error)
}

// chatBridge is the state behind the three methods: which conversations have
// a turn running through this bridge.
type chatBridge struct {
	backend  ChatBackend
	instance string

	mu     sync.Mutex
	active map[string]context.CancelFunc // conversation -> stop
}

// SetChatBackend installs the backend behind chat.send / chat.stop /
// chat.events and registers the methods; nil takes them away again, so
// "there is a backend" and "ping advertises chat" are one fact.
func (s *Server) SetChatBackend(b ChatBackend) {
	if b == nil {
		s.handlerMu.Lock()
		delete(s.handlers, MethodChatSend)
		delete(s.handlers, MethodChatStop)
		delete(s.handlers, MethodChatEvents)
		delete(s.handlers, MethodChatUIAction)
		delete(s.handlers, MethodChatUISync)
		delete(s.handlers, MethodChatUIJournal)
		delete(s.methodTimeouts, MethodChatUIJournal)
		delete(s.methodTimeouts, MethodChatSend)
		delete(s.methodTimeouts, MethodChatEvents)
		s.handlerMu.Unlock()
		return
	}
	cb := &chatBridge{backend: b, instance: "ext-" + randHex(6), active: map[string]context.CancelFunc{}}
	s.HandleRequestWithTimeout(MethodChatSend, cb.handleSend, chatSendTimeout)
	s.HandleRequestWithTimeout(MethodChatEvents, cb.handleEvents, chatQueryTimeout)
	s.HandleRequest(MethodChatStop, cb.handleStop)
	if ub, ok := b.(ChatUIBackend); ok {
		s.HandleRequestWithTimeout(MethodChatUIAction, cb.uiAction(ub), chatQueryTimeout)
		s.HandleRequestWithTimeout(MethodChatUISync, cb.uiSync(ub), chatQueryTimeout)
		s.HandleRequestWithTimeout(MethodChatUIJournal, cb.uiJournal(ub), chatQueryTimeout)
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func invalidParam(format string, a ...any) error {
	return &RequestError{Code: CodeInvalidInput, Err: fmt.Errorf(format, a...)}
}

// chatProfile reads the optional profile param.
func chatProfile(req *Request) (string, error) {
	p := req.String("profile")
	if p == "" {
		return "", nil
	}
	if !profiledir.ValidProfileID(p) || !safeArgValue(p) || strings.Contains(p, "=") {
		return "", invalidParam("invalid profile %q", firstLine(p))
	}
	return p, nil
}

// chooseProfileMessage is what the person is told when a request names no
// profile. The bridge never picks one for them: the side panel's selector does.
const chooseProfileMessage = "choose a profile in the extension"

// requireProfile is chatProfile for a method that acts on a profile: a
// request that names none is refused here, before any CLI is run.
func requireProfile(req *Request) (string, error) {
	p, err := chatProfile(req)
	if err != nil {
		return "", err
	}
	if p == "" {
		return "", invalidParam("%s", chooseProfileMessage)
	}
	return p, nil
}

// chatConversation reads the conversation param; required when must.
func chatConversation(req *Request, must bool) (string, error) {
	c := req.String("conversation")
	if c == "" && !must {
		return "", nil
	}
	if !chatIDPattern.MatchString(c) {
		return "", invalidParam("invalid conversation id")
	}
	return c, nil
}

// pageContext is the optional page the person is looking at.
type pageContext struct {
	URL, Title, Text, Selection string
	// A video page: Video says the extension tried to read the captions;
	// Transcript is empty when there were none.
	Video                                     bool
	VideoID, Channel, Description, Transcript string
}

var videoIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

func chatContext(req *Request) (pageContext, error) {
	var pc pageContext
	raw, ok := req.Params["context"]
	if !ok || raw == nil {
		return pc, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return pc, invalidParam("context must be an object")
	}
	read := func(key string, max int) (string, error) {
		v, present := m[key]
		if !present || v == nil {
			return "", nil
		}
		s, ok := v.(string)
		if !ok {
			return "", invalidParam("context.%s must be a string", key)
		}
		// Too long is shortened, at a character boundary: a page in a
		// script that takes 3 bytes a character is as welcome as a Latin one.
		return truncateUTF8(s, max), nil
	}
	var err error
	if pc.URL, err = read("url", chatMaxRawURL+1); err != nil {
		return pc, err
	}
	if pc.Title, err = read("title", chatMaxCtxTitle); err != nil {
		return pc, err
	}
	if pc.Text, err = read("text", chatMaxCtxText); err != nil {
		return pc, err
	}
	if pc.Selection, err = read("selection", chatMaxCtxSel); err != nil {
		return pc, err
	}
	if v, ok := m["video"].(bool); ok && v {
		pc.Video = true
		if id, _ := m["video_id"].(string); videoIDPattern.MatchString(id) {
			pc.VideoID = id
		}
		if pc.Channel, err = read("channel", chatMaxCtxChannel); err != nil {
			return pc, err
		}
		if pc.Description, err = read("description", chatMaxCtxDesc); err != nil {
			return pc, err
		}
		if pc.Transcript, err = read("transcript", chatMaxCtxTranscript); err != nil {
			return pc, err
		}
	}
	pc.URL = redactURL(plainField(pc.URL, false))
	if isFileURL(pc.URL) {
		pc = pageContext{URL: pc.URL, Title: pc.Title}
	}
	return pc, nil
}

type chatSend struct {
	profile, conversation, runtime, model, message string
	pc                                             pageContext
	images                                         []chatImage
}

func parseChatSend(req *Request) (chatSend, error) {
	var c chatSend
	var err error
	if c.profile, err = requireProfile(req); err != nil {
		return c, err
	}
	if c.conversation, err = chatConversation(req, false); err != nil {
		return c, err
	}
	c.runtime, c.model = req.String("runtime"), req.String("model")
	if c.runtime != "" && !capturesummary.ValidRuntimeID(c.runtime) {
		return c, invalidParam("invalid runtime")
	}
	if c.model != "" && (len(c.model) > chatMaxModelBytes || !safeArgValue(c.model)) {
		return c, invalidParam("invalid model")
	}
	if c.conversation == "" && c.runtime == "" {
		return c, invalidParam("chat.send needs a runtime to start a conversation")
	}
	if c.message = req.String("message"); c.message == "" {
		return c, invalidParam("chat.send needs a message")
	}
	if len(c.message) > chatMaxMessage {
		return c, invalidParam("message is longer than %d bytes", chatMaxMessage)
	}
	if c.pc, err = chatContext(req); err != nil {
		return c, err
	}
	c.images, err = chatImages(req)
	return c, err
}

// claim marks conversation as running a turn here, or reports it busy.
func (cb *chatBridge) claim(conversation string, stop context.CancelFunc) bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if _, busy := cb.active[conversation]; busy {
		return false
	}
	cb.active[conversation] = stop
	return true
}

func (cb *chatBridge) release(conversation string) {
	cb.mu.Lock()
	delete(cb.active, conversation)
	cb.mu.Unlock()
}

func errBusy() error {
	return &RequestError{Code: CodeBusy, Err: errors.New("this conversation is already answering")}
}

func (cb *chatBridge) handleSend(ctx context.Context, req *Request, progress ProgressFunc) (any, error) {
	p, err := parseChatSend(req)
	if err != nil {
		return nil, err
	}
	conv := p.conversation
	if conv == "" {
		if conv, err = cb.backend.CreateConversation(ctx, p.profile, p.runtime, p.model); err != nil {
			return nil, err
		}
		if !chatIDPattern.MatchString(conv) {
			return nil, fmt.Errorf("chat backend returned an unusable conversation id")
		}
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	if !cb.claim(conv, stop) {
		return nil, errBusy()
	}
	defer cb.release(conv)

	spec := ChatTurnSpec{
		Profile: p.profile, Conversation: conv, Turn: "ext-t-" + randHex(8),
		Instance: cb.instance, Message: withPageContext(p.pc, p.message),
	}
	if len(p.images) > 0 {
		files, cleanup, ierr := writeChatImages(p.images)
		if ierr != nil {
			return nil, ierr
		}
		defer cleanup()
		spec.Attach = files
		spec.Message += imagesNote(len(files))
	}
	var text strings.Builder
	var finished *chatevents.TurnFinishedPayload
	err = cb.backend.RunTurn(runCtx, spec, func(ev ChatEvent) {
		switch chatevents.EventType(ev.Type) {
		case chatevents.EventAssistantDelta:
			var d chatevents.AssistantDeltaPayload
			if json.Unmarshal(ev.Payload, &d) == nil && d.AgentID == "" {
				text.WriteString(d.Text)
			}
		case chatevents.EventTurnFinished:
			var f chatevents.TurnFinishedPayload
			if json.Unmarshal(ev.Payload, &f) == nil {
				finished = &f
			}
		}
		payload := ev.Payload
		if len(payload) == 0 {
			payload = json.RawMessage("null")
		}
		if b, merr := json.Marshal(struct {
			Seq          int64           `json:"seq"`
			Conversation string          `json:"conversation"`
			Turn         string          `json:"turn"`
			At           string          `json:"at,omitempty"`
			Payload      json.RawMessage `json:"payload"`
		}{ev.Seq, conv, spec.Turn, ev.At, payload}); merr == nil {
			progress(ev.Type, string(b))
		}
	})
	if err != nil {
		return nil, err
	}
	if finished != nil && finished.Status != chatevents.StatusCompleted {
		return nil, finishedError(finished)
	}
	return map[string]any{"conversation": conv, "turn": spec.Turn, "text": text.String()}, nil
}

// finishedError turns a turn that ended badly into the reply's error.
func finishedError(f *chatevents.TurnFinishedPayload) error {
	reason := f.Reason
	if reason == "" {
		reason = "the turn " + string(f.Status)
	}
	code := CodeInternal
	switch {
	case f.Code != "":
		code = f.Code
	case f.Status == chatevents.StatusCancelled:
		code = "cancelled"
	}
	return &RequestError{Code: code, Err: errors.New(reason)}
}

func (cb *chatBridge) handleStop(_ context.Context, req *Request, _ ProgressFunc) (any, error) {
	conv, err := chatConversation(req, true)
	if err != nil {
		return nil, err
	}
	cb.mu.Lock()
	stop := cb.active[conv]
	cb.mu.Unlock()
	if stop != nil {
		stop()
	}
	return map[string]any{"stopped": stop != nil}, nil
}

func (cb *chatBridge) handleEvents(ctx context.Context, req *Request, _ ProgressFunc) (any, error) {
	profile, err := requireProfile(req)
	if err != nil {
		return nil, err
	}
	conv, err := chatConversation(req, true)
	if err != nil {
		return nil, err
	}
	turn := req.String("turn")
	if turn != "" && !chatIDPattern.MatchString(turn) {
		return nil, invalidParam("invalid turn id")
	}
	after := req.Int("after_seq", 0)
	if after < 0 {
		return nil, invalidParam("after_seq must not be negative")
	}
	page, err := cb.backend.ListEvents(ctx, profile, conv, turn, int64(after))
	if err != nil {
		return nil, err
	}
	if page.Events == nil {
		page.Events = []ChatEvent{}
	}
	return page, nil
}
