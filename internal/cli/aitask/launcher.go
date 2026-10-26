package aitask

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/aitask"
	chatcli "github.com/monoes/mono-agent/internal/cli/chat"
	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// selfPath is the binary the launcher runs: this one. A var so tests can stand in for it.
var selfPath = os.Executable

// selfLauncher is aitask.Launcher over this binary: the conversation is made
// by `chat history create --mode coder` (the code `coder queue start` uses)
// and the turn is `chat --conversation … --turn …`, run by a detached
// `aitask supervise` so the end of the turn can be recorded.
type selfLauncher struct{ d *Deps }

func (l selfLauncher) global() []string {
	return []string{"--db-path=" + l.d.DBPath(), "--profile=" + l.d.ProfileID()}
}

func (l selfLauncher) Ready(context.Context) error {
	db, err := l.d.OpenDB()
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}
	defer db.Close()
	s, err := chatcli.LoadSettings(db.DB)
	if err != nil {
		return err
	}
	if !s.Enabled {
		return &aitask.Error{Code: aitask.CodeCoderDisabled,
			Message: "coder mode is disabled, so there is no coder to hand this to: the caller can run it as a one-shot instead, or you can enable coder mode in Settings",
			Fields:  map[string]any{"fallback": "oneshot"}}
	}
	return nil
}

// Policy fingerprints the coder settings in force.
func (l selfLauncher) Policy(context.Context) (string, error) {
	db, err := l.d.OpenDB()
	if err != nil {
		return "", fmt.Errorf("initializing database: %w", err)
	}
	defer db.Close()
	s, err := chatcli.LoadSettings(db.DB)
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Confined is false: nothing here verifies a confinement (sandboxprobe only
// guesses), so no profile runs unattended tasks yet.
func (l selfLauncher) Confined(context.Context) bool { return false }

func (l selfLauncher) DefaultBudget(context.Context) (float64, error) {
	db, err := l.d.OpenDB()
	if err != nil {
		return 0, fmt.Errorf("initializing database: %w", err)
	}
	defer db.Close()
	s, err := chatcli.LoadSettings(db.DB)
	if err != nil {
		return 0, err
	}
	return s.BudgetUSD, nil
}

// conversationLevel is the `chat history create` flags a task's conversation
// is made with. A confined kind keeps the coder conversation it always had:
// its turns are narrowed to the kind's own profile and command allow-list
// (chat.ConfinedKind), whatever the level. Any other kind gets the level it
// asked for (coding or workspace), and qa has no profile to run in there, so
// it is refused rather than run with a shell.
func conversationLevel(kind, access string) ([]string, error) {
	if chatcli.ConfinedKind(kind) {
		return []string{"--mode", "coder"}, nil
	}
	switch access {
	case aitask.AccessCoding:
		return []string{"--access", ai.AccessCoding}, nil
	case aitask.AccessWorkspace:
		return []string{"--access", ai.AccessWorkspace}, nil
	}
	msg := fmt.Sprintf("a %s task cannot run at the %q access level: only a confined kind has a read-only profile to run it in, and a coder turn has a shell.", kind, access)
	if k, err := aitask.Lookup(kind); err == nil {
		var ok []string
		for _, a := range []string{aitask.AccessCoding, aitask.AccessWorkspace} {
			if aitask.AccessRank(a) <= aitask.AccessRank(k.Ceiling()) {
				ok = append(ok, a)
			}
		}
		if len(ok) > 0 {
			msg += " Submit it with --access " + strings.Join(ok, " or ") + "."
		} else {
			msg += " This kind has no level it can run at."
		}
	}
	return nil, &aitask.Error{Code: aitask.CodeAccessUnenforced, Message: msg}
}

// Enforceable is nil when a turn of kind can be held to access.
func (l selfLauncher) Enforceable(kind, access string) error {
	_, err := conversationLevel(kind, access)
	return err
}

func (l selfLauncher) CreateConversation(ctx context.Context, runtime, kind, access string) (aitask.Conversation, error) {
	level, err := conversationLevel(kind, access)
	if err != nil {
		return aitask.Conversation{}, err
	}
	args := append(append(l.global(), "--json", "chat", "history", "create"), level...)
	args = append(args, "--profile-folder")
	if runtime != "" {
		args = append(args, "--runtime", runtime)
	}
	out, err := runSelf(ctx, args, nil, nil)
	if err != nil {
		return aitask.Conversation{}, err
	}
	var rec ai.ConversationRecord
	if err := json.Unmarshal(out, &rec); err != nil || rec.ID == "" || rec.Cwd == "" {
		return aitask.Conversation{}, fmt.Errorf("chat history create printed no conversation: %s", strings.TrimSpace(string(out)))
	}
	return aitask.Conversation{ID: rec.ID, Cwd: rec.Cwd, Runtime: rec.RuntimeID}, nil
}

func (l selfLauncher) Detach(_ context.Context, taskID, token string) error {
	bin, err := selfPath()
	if err != nil {
		return fmt.Errorf("cannot locate monoagentcli: %w", err)
	}
	cmd := exec.Command(bin, append(l.global(), "aitask", "supervise", taskID)...)
	cmd.Env = append(os.Environ(), aitask.EnvTaskID+"="+taskID, aitask.EnvToken+"="+token)
	if err := clicore.DetachStart(cmd); err != nil {
		return fmt.Errorf("starting the task's turn: %w", err)
	}
	return cmd.Process.Release()
}

// runSelf runs this binary with args, and returns stdout; a failure carries
// the {"error"} line it printed.
func runSelf(ctx context.Context, args []string, stdin io.Reader, env []string) ([]byte, error) {
	bin, err := selfPath()
	if err != nil {
		return nil, fmt.Errorf("cannot locate monoagentcli: %w", err)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = stdin
	// A cancelled turn is asked to stop (it winds its agent down), and killed,
	// pipes and all, if it has not within the delay.
	cmd.Cancel = func() error {
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = 3 * time.Second
	if env != nil {
		cmd.Env = env
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var refusal struct{ Error, Code string }
		lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
		_ = json.Unmarshal(lines[len(lines)-1], &refusal)
		msg := refusal.Error
		if msg == "" {
			msg = strings.TrimSpace(stderr.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		if refusal.Code == aitask.CodeCoderDisabled {
			return nil, &aitask.Error{Code: aitask.CodeCoderDisabled, Message: msg, Fields: map[string]any{"fallback": "oneshot"}}
		}
		return nil, errors.New(msg)
	}
	return out, nil
}

// runTurn runs task t's first turn as the child of the supervisor and waits
// for it. It stops the turn when the task is no longer running (cancelled).
func runTurn(ctx context.Context, d *Deps, svc *aitask.Service, t aitask.Task) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			if cur, err := svc.Store.Get(t.ID); err == nil && cur.Status != aitask.StatusRunning {
				cancel()
				return
			}
		}
	}()
	args := append(selfLauncher{d}.global(), "chat", "--conversation", t.ConversationID, "--turn", uuid.NewString(), "--prompt-stdin",
		"--budget-usd", strconv.FormatFloat(t.BudgetUSD, 'f', -1, 64))
	_, err := runSelf(ctx, args, strings.NewReader(t.Prompt), os.Environ())
	return err
}
