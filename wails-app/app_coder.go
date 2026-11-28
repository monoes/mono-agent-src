package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/monoes/mono-agent/internal/ai"
)

// ─────────────────────────────────────────────────────────────────────────────
// Coder mode (issue #203): a chat conversation in which a coding runtime
// (claude, codex, opencode, …) runs with full access inside one folder. Everything here shells out to
// `monoagentcli coder …` / `chat history create --mode coder` (#202) and
// returns the CLI's stdout JSON verbatim; the only logic is building args.
// A coder conversation's turns go through StartChatTurn like any other,
// with tools=false: the CLI refuses --tools for them.
// ─────────────────────────────────────────────────────────────────────────────

// jsonCLI runs `monoagentcli [--profile P] --json <args>` and returns its
// stdout. Unlike chatSupervisor.cli it keeps the {"error","code"} object a
// --json failure prints on stdout, so codes such as coder_disabled and
// needs_monomind_update reach the frontend. On failure it still returns the
// stdout it got, for commands whose report is their output even then (org
// validate).
func (a *App) jsonCLI(args ...string) (string, error) {
	return a.jsonCLIStdin("", args...)
}

// jsonCLIStdin is jsonCLI with stdin fed to the command (none when empty).
func (a *App) jsonCLIStdin(stdin string, args ...string) (string, error) {
	if a.chatSup == nil {
		return "", fmt.Errorf("chat supervisor not initialized")
	}
	bin, err := a.chatSup.findCLI()
	if err != nil {
		return "", err
	}
	full, err := a.scopedArgs(args...)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), chatCLITimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, full...)
	suppressConsole(cmd)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	return cliAnswer(args, out, stderr.Bytes(), err)
}

// cliAnswer reads what a --json command run for args printed and how it ended.
func cliAnswer(args []string, out, stderr []byte, err error) (string, error) {
	trimmed := strings.TrimSpace(string(out))
	if err != nil {
		var refusal chatRefusal
		_ = json.Unmarshal([]byte(lastLine(string(out))), &refusal)
		msg := refusal.Error
		if msg == "" {
			msg = lastLine(string(stderr))
		}
		if msg == "" {
			msg = err.Error()
		}
		if refusal.Code != "" {
			return trimmed, &codedError{msg: msg, code: refusal.Code}
		}
		return trimmed, errors.New(msg)
	}
	if !looksLikeJSON(trimmed) {
		return "", fmt.Errorf("monoagentcli %s: unexpected output: %q", strings.Join(args, " "), trimmed)
	}
	return trimmed, nil
}

// jsonResult is jsonCLI's stdout, or its error in aiError's shape.
func (a *App) jsonResult(args ...string) string {
	out, err := a.jsonCLI(args...)
	if err != nil {
		return aiError(err)
	}
	return out
}

// CoderStatus returns `coder status`: enabled, workspace root, defaults,
// and whether monomind has the capabilities coder mode needs (ready).
func (a *App) CoderStatus() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("coder", "status")
}

// CoderEnable turns coder mode on. The frontend only calls it after the user
// confirmed the risk dialog, which is what --yes-i-understand records.
func (a *App) CoderEnable() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("coder", "enable", "--yes-i-understand")
}

func (a *App) CoderDisable() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("coder", "disable")
}

// coderSetArgs builds `coder set`. An empty workspace root, maxTurns <= 0,
// an empty timeout or an empty orgRouter leave that setting unchanged;
// budgetUsd 0 clears the budget and a negative one leaves it unchanged.
// orgRouter is the dynamic org's router: auto, jev or model (the CLI checks).
// App access has its own binding, CoderSetAppAccess.
func coderSetArgs(workspaceRoot string, maxTurns int, timeout string, budgetUsd float64, orgRouter string) []string {
	args := []string{"coder", "set"}
	if workspaceRoot != "" {
		args = append(args, "--workspace-root", workspaceRoot)
	}
	if maxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(maxTurns))
	}
	if timeout != "" {
		args = append(args, "--timeout", timeout)
	}
	if budgetUsd >= 0 {
		args = append(args, "--budget-usd", strconv.FormatFloat(budgetUsd, 'f', -1, 64))
	}
	if orgRouter != "" {
		args = append(args, "--org-router", orgRouter)
	}
	return args
}

func (a *App) CoderSet(workspaceRoot string, maxTurns int, timeout string, budgetUsd float64, orgRouter string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult(coderSetArgs(workspaceRoot, maxTurns, timeout, budgetUsd, orgRouter)...)
}

// CoderSetAppAccess is `coder set --app-access on|off` (coder.appAccess,
// default on): whether coder prompts carry the profile summary and a dynamic
// org's workers are offered MonoAgent's surfaces. It changes what agents are
// told, not what they can do (see `coder set --help`). The CLI refuses anyone
// but the user. Returns coder status, whose appAccess CoderStatus also reads
// back.
func (a *App) CoderSetAppAccess(on bool) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	v := "off"
	if on {
		v = "on"
	}
	return a.jsonResult("coder", "set", "--app-access", v)
}

// CoderWorkspaceRoot sets up the coder root itself as the chat's working
// folder, shared by every chat that picks it, with runtimeID's setup files
// (the CLI's default runtime when empty): {path, created, git,
// init:{created, skipped}}.
func (a *App) CoderWorkspaceRoot(runtimeID string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	args := []string{"coder", "workspace", "root"}
	if runtimeID != "" {
		args = append(args, "--runtime", runtimeID)
	}
	return a.jsonResult(args...)
}

// CoderWorkspaceDefault sets up the active profile's own folder as the
// chat's working folder, with runtimeID's setup files (add-only), and returns
// {path, profile:{id,name}, source, created, init}. It is the default folder
// of a new coder chat; the user may pick another.
func (a *App) CoderWorkspaceDefault(runtimeID string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	args := []string{"coder", "workspace", "default"}
	if runtimeID != "" {
		args = append(args, "--runtime", runtimeID)
	}
	return a.jsonResult(args...)
}

// CoderWorkspaceList returns the folders coder conversations used, newest
// first.
func (a *App) CoderWorkspaceList() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("coder", "workspace", "list")
}

// CoderStopBackground stops the background processes a coder turn left
// running (its coder.background notice): {stopped, gone, refused} pids. The
// CLI only touches pids that turn reported and that still carry its marker.
func (a *App) CoderStopBackground(conversationID, turnID string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("coder", "stop-background", "--conversation", conversationID, "--turn", turnID)
}

// SetChatOrgMode switches a coder conversation between working alone
// ("solo") and a dynamic org ("dynamic", #226) from its next turn on:
// `chat history set-org`.
func (a *App) SetChatOrgMode(conversationID, mode string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("chat", "history", "set-org", conversationID, mode)
}

// AnswerAgentQuestion answers a dynamic-org worker's question (#256):
// `chat turn answer`.
func (a *App) AnswerAgentQuestion(conversationID, turnID, agentID, questionID, text string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("chat", "turn", "answer", conversationID, turnID, "--agent", agentID, "--question", questionID, "--text", text)
}

// coderConversationArgs builds `chat history create` for a coder
// conversation: either in cwd, or (newWorkspace) in a folder the CLI
// creates. Coder chats belong to the general assistant's history. An empty
// model or effort leaves the runtime's default.
func coderConversationArgs(runtimeID, model, effort, cwd string, newWorkspace bool) []string {
	args := []string{"chat", "history", "create", "--runtime", runtimeID, "--workflow", "general", "--mode", "coder"}
	if newWorkspace {
		args = append(args, "--new-workspace")
	} else {
		args = append(args, "--cwd", cwd)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort != "" {
		args = append(args, "--effort", effort)
	}
	return args
}

// CreateCoderConversation creates a coder conversation and returns it in
// CreateChatConversation's shape (mode "coder", cwd its folder).
func (a *App) CreateCoderConversation(runtimeID, model, effort, cwd string, newWorkspace bool) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if !newWorkspace && cwd == "" {
		return aiError(fmt.Errorf("choose a folder for the coder conversation"))
	}
	out, err := a.jsonCLI(coderConversationArgs(runtimeID, model, effort, cwd, newWorkspace)...)
	if err != nil {
		return aiError(err)
	}
	var rec ai.ConversationRecord
	if err := json.Unmarshal([]byte(out), &rec); err != nil {
		return aiError(fmt.Errorf("monoagentcli chat history create: unexpected output: %w", err))
	}
	b, _ := json.Marshal(rec.Conversation())
	return string(b)
}

// PickCoderFolder opens the native folder picker; "" when cancelled.
func (a *App) PickCoderFolder() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "Choose a folder for Coder mode"})
	if err != nil {
		return ""
	}
	return dir
}
