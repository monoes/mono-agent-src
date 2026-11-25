package extension

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// cliChatBackend is the real ChatBackend: it runs this binary's own CLI, the
// way the desktop app's chat supervisor does (wails-app/app_chat.go). Tests
// replace exec with a fake; nothing here opens the database.
type cliChatBackend struct {
	// bin is the executable to run; "" means this one.
	bin string
}

// NewCLIChatBackend returns the backend that execs this binary's `chat`
// commands. It refuses to run unless the process is monoagentcli.
func NewCLIChatBackend() ChatBackend { return &cliChatBackend{} }

func (b *cliChatBackend) binary() (string, error) {
	if b.bin != "" {
		return b.bin, nil
	}
	bin, err := selfExecutable()
	if err != nil {
		return "", Unavailable("cannot locate monoagentcli: %v", err)
	}
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(bin)), ".exe")
	if !strings.HasPrefix(base, "monoagentcli") {
		return "", Unavailable("chat needs the monoagentcli bridge (running as %s)", filepath.Base(bin))
	}
	return bin, nil
}

// chatToolsMode is the --tools value of every turn the extension starts, with
// or without page text attached: the page-read set (three read-only workflow
// tools; see cmd/monoagentcli/chat_pageread.go). One mode for all of the
// panel's turns, so a later message in the same conversation can never widen
// what a page reached the model with.
const chatToolsMode = "monoagent:read"

// chatBase is the global flags every call carries.
func chatBase(profile string) []string {
	return []string{"--profile=" + profile, "--json"}
}

func chatTurnArgv(s ChatTurnSpec) []string {
	// The message, page text included, goes over stdin: argv is world-readable
	// in a process listing and has a size limit.
	argv := append(chatBase(s.Profile), "chat", "--conversation="+s.Conversation, "--turn="+s.Turn,
		"--instance="+s.Instance, "--tools="+chatToolsMode, "--prompt-stdin")
	for _, p := range s.Attach {
		argv = append(argv, "--attach="+p)
	}
	return argv
}

// run execs the CLI to completion and returns stdout.
func (b *cliChatBackend) run(ctx context.Context, args ...string) ([]byte, error) {
	bin, err := b.binary()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.WaitDelay = monomindWaitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, &RequestError{Code: CodeTimeout, Err: ctx.Err()}
		}
		return nil, cliFailure(stdout.Bytes(), stderr.String(), err)
	}
	return stdout.Bytes(), nil
}

// cliFailure explains a failed call, keeping the CLI's own message.
func cliFailure(stdout []byte, stderr string, err error) error {
	var refusal struct{ Error, Code string }
	_ = json.Unmarshal(lastJSONLine(stdout), &refusal)
	msg := refusal.Error
	if msg == "" {
		msg = strings.TrimSpace(stderr)
	}
	if isProfileGateRefusal(msg) {
		return invalidParam("%s", chooseProfileMessage)
	}
	if len(msg) > stderrTail {
		msg = "…" + msg[len(msg)-stderrTail:]
	}
	if msg == "" {
		msg = err.Error()
	}
	code := refusal.Code
	if code == "" && strings.Contains(msg, "active turn") {
		code = CodeBusy
	}
	if code == "" {
		return errors.New(msg)
	}
	return &RequestError{Code: code, Err: errors.New(msg)}
}

// isProfileGateRefusal recognises the CLI's refusal of a profile-scoped
// command run without --profile. Matched on the text: exit code 3 is the
// generic invalid-input code and says nothing about which input.
func isProfileGateRefusal(msg string) bool {
	return strings.Contains(msg, "acts on a profile")
}

func lastJSONLine(b []byte) []byte {
	lines := bytes.Split(bytes.TrimSpace(b), []byte("\n"))
	return lines[len(lines)-1]
}

// chatCreateArgv makes a panel conversation. The access is named, not left to
// a default: the panel's chat is scoped (chatToolsMode on every turn), and
// only the operator can raise it afterwards (`chat history set-access`).
func chatCreateArgv(profile, runtime, model string) []string {
	args := append(chatBase(profile), "chat", "history", "create", "--runtime="+runtime, "--workflow=general", "--access=scoped")
	if model != "" {
		args = append(args, "--model="+model)
	}
	return args
}

func (b *cliChatBackend) CreateConversation(ctx context.Context, profile, runtime, model string) (string, error) {
	out, err := b.run(ctx, chatCreateArgv(profile, runtime, model)...)
	if err != nil {
		return "", err
	}
	var conv struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(out, &conv) != nil || conv.ID == "" {
		return "", errors.New("chat history create returned no conversation id")
	}
	return conv.ID, nil
}

func (b *cliChatBackend) RunTurn(ctx context.Context, spec ChatTurnSpec, onEvent func(ChatEvent)) error {
	bin, err := b.binary()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, chatTurnArgv(spec)...)
	cmd.WaitDelay = monomindWaitDelay
	cmd.Stdin = strings.NewReader(spec.Message)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start monoagentcli: %w", err)
	}
	finished, admitted, refusal := readTurn(stdout, onEvent)
	waitErr := cmd.Wait()

	if ctx.Err() != nil {
		if admitted && !finished {
			b.finish(spec, chatevents.StatusCancelled, "stopped from the side panel")
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return &RequestError{Code: CodeTimeout, Err: ctx.Err()}
		}
		return &RequestError{Code: "cancelled", Err: errors.New("the turn was stopped")}
	}
	if !admitted {
		return cliFailure(refusal, stderr.String(), orExit(waitErr))
	}
	if !finished {
		reason := "monoagentcli ended without finishing the turn"
		if waitErr != nil {
			reason = "monoagentcli exited: " + waitErr.Error()
		}
		b.finish(spec, chatevents.StatusFailed, reason)
		return errors.New(reason)
	}
	return nil
}

func orExit(err error) error {
	if err != nil {
		return err
	}
	return errors.New("monoagentcli exited before starting the turn")
}

// readTurn reads the admission line then the event lines. It returns whether
// turn.finished was seen, whether the turn was admitted, and, when it was
// not, the raw first line (the {"error","code"} refusal).
func readTurn(r interface{ Read([]byte) (int, error) }, onEvent func(ChatEvent)) (finished, admitted bool, first []byte) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		if !admitted {
			var adm struct {
				Admitted bool `json:"admitted"`
				Existed  bool `json:"existed"`
				Turn     struct {
					ID string `json:"id"`
				} `json:"turn"`
			}
			if json.Unmarshal(line, &adm) != nil || adm.Turn.ID == "" {
				return false, false, append([]byte(nil), line...)
			}
			if adm.Existed {
				return false, false, []byte(`{"error":"that turn already ran"}`)
			}
			admitted = true
			continue
		}
		var rec chatevents.Record
		if json.Unmarshal(line, &rec) != nil || rec.Type == "" {
			continue
		}
		if rec.Type == chatevents.EventTurnFinished {
			finished = true
		}
		onEvent(ChatEvent{Seq: rec.Seq, Type: string(rec.Type), Payload: rec.Payload, At: rec.At})
	}
	return finished, admitted, first
}

// finish records a turn whose process ended without its own turn.finished.
func (b *cliChatBackend) finish(spec ChatTurnSpec, st chatevents.TurnStatus, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, _ = b.run(ctx, append(chatBase(spec.Profile), "chat", "history", "finish",
		"--status="+string(st), "--reason="+reason, "--", spec.Conversation, spec.Turn)...)
}

func (b *cliChatBackend) ListEvents(ctx context.Context, profile, conversation, turn string, afterSeq int64) (ChatEventsPage, error) {
	if turn == "" {
		turns, err := b.turnIDs(ctx, profile, conversation)
		if err != nil {
			return ChatEventsPage{}, err
		}
		// "The latest turn" is the latest one of the conversation itself: the
		// cards' own journal turns are read with chat.ui_journal.
		for _, id := range turns {
			if !isJournalTurn(id) {
				turn = id
				break
			}
		}
		if turn == "" {
			return ChatEventsPage{Events: []ChatEvent{}}, nil
		}
	}
	out, err := b.run(ctx, append(chatBase(profile), "chat", "history", "events",
		"--after-seq="+strconv.FormatInt(afterSeq, 10), "--", conversation, turn)...)
	if err != nil {
		return ChatEventsPage{}, err
	}
	var page struct {
		Items []chatevents.Record `json:"items"`
		Turn  struct {
			Status string `json:"status"`
		} `json:"turn"`
	}
	if json.Unmarshal(out, &page) != nil {
		return ChatEventsPage{}, errors.New("chat history events returned unreadable output")
	}
	res := ChatEventsPage{Turn: turn, Events: make([]ChatEvent, 0, len(page.Items)), TurnActive: page.Turn.Status == "active"}
	for _, r := range page.Items {
		res.Events = append(res.Events, ChatEvent{Seq: r.Seq, Type: string(r.Type), Payload: r.Payload, At: r.At})
	}
	return res, nil
}
