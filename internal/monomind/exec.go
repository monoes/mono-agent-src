package monomind

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/logging"
)

// KillGrace bounds the window between a graceful cancel (cancel frame +
// stdin EOF) and the process-group kill on ctx cancellation. Overridable
// for tests.
var KillGrace = 5 * time.Second

// FullAccessKillGrace is KillGrace for an --access full (or read) turn. monomind runs
// that agent in its own process group and kills the whole tree itself on
// SIGTERM (SIGTERM, then SIGKILL after 5s), so it needs more than 6s; a
// group kill of monomind alone never reaches the agent (protocol §3).
var FullAccessKillGrace = 12 * time.Second

// ErrSandboxRequired is returned, wrapped, when ExecOptions.RequireSandbox
// is set and the sandbox can't be applied.
var ErrSandboxRequired = errors.New("the required sandbox is not available")

// Exec runs one agent turn and invokes onEvent for every protocol event in
// arrival order. It returns the turn's terminal state: a *ProtocolError for
// error turns (also mirrored in the error event the handler received).
//
// Lifecycle contract (protocol §3.2/§3.4): the stream always ends in done;
// ctx cancellation sends a cancel frame first (best-effort graceful), then
// escalates to a process-group kill after a grace window so neither
// monomind nor an agent-CLI grandchild survives the caller.
//
// A locked monoes.me account runs no turn: Exec returns the account gate's
// error (the typed *account.LoginRequiredError) before it looks for monomind
// (spec section 6.2).
func Exec(ctx context.Context, opts ExecOptions, onEvent func(Event)) (*TurnResult, error) {
	if lrErr := requireAccount(ctx); lrErr != nil {
		return nil, lrErr
	}
	bin := opts.Bin
	if bin == "" {
		var err error
		bin, err = Find()
		if err != nil {
			return nil, err
		}
	}
	if err := CheckOutside(bin, opts.Cwd); err != nil {
		return nil, err
	}

	if opts.Prompt == "" {
		return nil, fmt.Errorf("ExecOptions.Prompt is required")
	}
	if len(opts.Tools) > 0 && opts.OnToolCall == nil {
		return nil, fmt.Errorf("ExecOptions.OnToolCall is required when Tools is set")
	}

	args := []string{"agent", "exec", "--runtime", opts.Runtime}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.Effort != "" {
		switch {
		case opts.EffortFlag:
			args = append(args, "--effort", opts.Effort)
		case opts.Runtime == "claude":
			args = append(args, "--env", "CLAUDE_EFFORT="+opts.Effort)
		}
	}
	cwd := opts.Cwd
	var sandboxArgs []string
	sandboxEffective, sandboxNote, fallbackActive := "", "", false
	if opts.Sandbox != "" {
		caps, _ := capabilitiesFor(ctx, bin) // a failed handshake: no sandbox, as before
		var modes []string
		if caps.Has(CapAgentExecSandbox) {
			modes = SandboxModesFor(ctx, opts.Runtime)
		}
		sandboxArgs, sandboxEffective, fallbackActive, sandboxNote = SandboxFallbackPlan(caps, modes, opts.Runtime, opts.Sandbox, SandboxFallbackSetting())
		if sandboxNote == SandboxFallbackRefusedNote {
			return nil, fmt.Errorf("the %s runtime has no %s sandbox and the sandbox fallback setting is 'fail' (change it in Settings to run without one)", opts.Runtime, opts.Sandbox)
		}
		if opts.RequireSandbox && sandboxEffective != SandboxStatusSandboxed {
			return nil, fmt.Errorf("%w: %s sandbox for %s is %s", ErrSandboxRequired, opts.Sandbox, opts.Runtime, sandboxEffective)
		}
	}
	if len(sandboxArgs) > 0 && cwd == "" && opts.WorkspacePurpose != "" && opts.Runtime != "claude" {
		dir, err := SandboxWorkspaceDir(opts.WorkspacePurpose)
		if err != nil {
			return nil, fmt.Errorf("sandbox workspace: %w", err)
		}
		cwd = dir
	}
	if cwd != "" {
		args = append(args, "--cwd", cwd)
	}
	args = append(args, sandboxArgs...)
	if opts.Resume != "" {
		args = append(args, "--resume", opts.Resume)
	}
	if len(opts.AllowBashPrefixes) > 0 {
		args = append(args, "--allow-bash-prefix", strings.Join(opts.AllowBashPrefixes, ","))
	}
	if opts.Access != "" {
		if opts.Access == AccessFull && opts.Cwd == "" {
			return nil, fmt.Errorf("ExecOptions.Access %q needs Cwd", opts.Access)
		}
		args = append(args, "--access", opts.Access)
	}
	if len(opts.Settings) > 0 {
		args = append(args, "--settings", strings.Join(opts.Settings, ","))
	}
	if opts.MaxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(opts.MaxTurns))
	}
	if opts.Timeout > 0 {
		args = append(args, "--timeout", formatDuration(opts.Timeout))
	}
	if opts.BudgetUSD > 0 {
		args = append(args, "--budget-usd", fmt.Sprintf("%g", opts.BudgetUSD))
	}
	if opts.ToolTimeout > 0 {
		args = append(args, "--tool-timeout", formatDuration(opts.ToolTimeout))
	}
	for _, p := range opts.Attach {
		args = append(args, "--attach", p)
	}

	cleanup := []func(){}
	defer func() {
		for _, f := range cleanup {
			f()
		}
	}()

	// The prompt always travels via --prompt-file (written to a temp file),
	// mirroring --system-file: large prompts (e.g. agentgen's HTML payload)
	// must never hit argv limits.
	tempDir := execTempDir(opts)
	promptF, err := os.CreateTemp(tempDir, "monoagent-prompt-*.md")
	if err != nil {
		return nil, fmt.Errorf("write prompt file: %w", err)
	}
	if _, err := promptF.WriteString(opts.Prompt); err != nil {
		promptF.Close()
		os.Remove(promptF.Name())
		return nil, fmt.Errorf("write prompt file: %w", err)
	}
	promptF.Close()
	promptName := promptF.Name()
	cleanup = append(cleanup, func() { os.Remove(promptName) })
	args = append(args, "--prompt-file", promptName)

	if opts.SystemPrompt != "" {
		f, err := os.CreateTemp(tempDir, "monoagent-system-*.md")
		if err != nil {
			return nil, fmt.Errorf("write system prompt: %w", err)
		}
		if _, err := f.WriteString(opts.SystemPrompt); err != nil {
			f.Close()
			os.Remove(f.Name())
			return nil, fmt.Errorf("write system prompt: %w", err)
		}
		f.Close()
		name := f.Name()
		cleanup = append(cleanup, func() { os.Remove(name) })
		args = append(args, "--system-file", name)
	}

	if len(opts.Tools) > 0 {
		b, err := json.Marshal(opts.Tools)
		if err != nil {
			return nil, fmt.Errorf("marshal tools: %w", err)
		}
		f, err := os.CreateTemp(tempDir, "monoagent-tools-*.json")
		if err != nil {
			return nil, fmt.Errorf("write tools file: %w", err)
		}
		if _, err := f.Write(b); err != nil {
			f.Close()
			os.Remove(f.Name())
			return nil, fmt.Errorf("write tools file: %w", err)
		}
		f.Close()
		name := f.Name()
		cleanup = append(cleanup, func() { os.Remove(name) })
		args = append(args, "--tools", "stdio", "--tools-file", name)
	} else {
		args = append(args, "--tools", "none")
	}

	cmd := exec.Command(bin, args...)
	// Always build a filtered environment (not only when opts.Env is set):
	// when a Claude Code session (this app itself, or anything upstream)
	// runs monoagentcli, its own CLAUDECODE/CLAUDE_CODE_*/CLAUDE_PID session
	// markers are ambient in the process environment — set globally for the
	// whole login session (confirmed directly: still present even in a
	// process launched via `open`, with PPID=1, completely detached from
	// any parent shell). The `claude` CLI reads these as "I'm already
	// running inside a Claude Code session" and changes its own auth
	// resolution accordingly, which surfaces as "Not logged in" for a
	// separate, independently-launched `claude` login — even though the
	// user's own Keychain-stored credentials are perfectly valid. Stripping
	// them here means every chat/agent turn gets a clean environment
	// regardless of what launched monoagentcli.
	cmd.Env = PinEnvIn(append(FilteredEnviron(), envSlice(opts.Env)...), bin, opts.Cwd)
	cmd.Env = SelfFirstOnPath(cmd.Env) // `monoagentcli` in the agent's shell is this build
	// The run this turn belongs to (a workflow execution, an org run) goes
	// to monomind as MONOAGENT_RUN_ID, so its lines can be joined with ours.
	cmd.Env = logging.ChildEnv(ctx, cmd.Env)
	if opts.Box != nil {
		boxed, err := boxedCommand(ctx, opts, bin, args, cmd.Env)
		if err != nil {
			return nil, err
		}
		cmd = boxed
	}
	setProcessGroup(cmd)
	logging.FromContext(ctx).Debug("monomind exec", "runtime", opts.Runtime, "model", opts.Model, "cwd", cwd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr // monomind keeps diagnostics off stdout (§3)
	if opts.Stderr != nil {
		cmd.Stderr = opts.Stderr
	}

	release, err := startProcessGroup(cmd)
	if err != nil {
		return nil, fmt.Errorf("start monomind: %w", err)
	}
	defer release()

	res := &TurnResult{SandboxStatus: sandboxEffective}
	events := make(chan Event, 64)
	var stdinMu sync.Mutex
	var stdinOnce sync.Once

	writeLine := func(b []byte) {
		stdinMu.Lock()
		defer stdinMu.Unlock()
		_, _ = stdin.Write(append(b, '\n'))
	}
	closeStdin := func() {
		stdinOnce.Do(func() {
			stdinMu.Lock()
			defer stdinMu.Unlock()
			_ = stdin.Close()
		})
	}

	// stdout reader: one JSON object per line (§3); closes events on exit.
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer close(events)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 || line[0] != '{' {
				continue // tolerate stray non-JSON output
			}
			lineCopy := make([]byte, len(line))
			copy(lineCopy, line) // sc.Bytes() is reused across scans
			var ev Event
			if err := json.Unmarshal(lineCopy, &ev); err != nil {
				continue // malformed line: skip, keep streaming
			}
			select {
			case events <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Event loop: fan out to the caller, bridge tool calls, capture state.
	var toolWg sync.WaitGroup
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		for ev := range events {
			if ev.Type == EventStart && sandboxEffective != "" {
				ev.SandboxStatus = sandboxStatus(sandboxEffective, ev.SandboxFields)
				if fallbackActive && ev.SandboxApplied != "" {
					ev.SandboxStatus = AppliedSandboxStatus(opts.Sandbox, ev.SandboxApplied)
				}
				ev.SandboxNote = sandboxNote
			}
			if onEvent != nil {
				onEvent(ev)
			}
			ApplyEventToResult(res, ev)
			switch ev.Type {
			case EventToolCall:
				if opts.OnToolCall == nil {
					// Protocol mismatch: the subprocess emitted a tool_call
					// even though this turn never enabled tools (opts.Tools
					// is empty, so opts.OnToolCall is nil too — the common
					// no-tools case). Calling a nil handler here would
					// panic in this background goroutine and crash the
					// entire hosting process (CLI or Wails GUI backend),
					// not just this turn. Report it as a protocol error
					// instead, and reply with an ok:false tool_result frame
					// so a real subprocess waiting on the round-trip can
					// react instead of hanging.
					res.Err = &ProtocolError{
						Code:    ErrBadFrame,
						Message: fmt.Sprintf("received tool_call %q for %q with no OnToolCall handler configured (Tools not set on this turn)", ev.ID, ev.Name),
						Fatal:   true,
					}
					frame := toolResultFrame{V: ProtocolVersion, Type: "tool_result", ID: ev.ID}
					frame.OK = false
					frame.Result.Text = "no tool handler configured for this turn"
					if b, err := json.Marshal(frame); err == nil {
						writeLine(b)
					}
					continue
				}
				toolWg.Add(1)
				go func(ev Event) {
					defer toolWg.Done()
					text, err := opts.OnToolCall(ctx, ev.Name, ev.Args)
					frame := toolResultFrame{V: ProtocolVersion, Type: "tool_result", ID: ev.ID}
					if err != nil {
						frame.OK = false
						frame.Result.Text = err.Error()
					} else {
						frame.OK = true
						frame.Result.Text = text
					}
					if b, err := json.Marshal(frame); err == nil {
						writeLine(b)
					}
				}(ev)
			}
		}
	}()

	// cmd.Wait must not run concurrently with the reader goroutine: per the
	// os/exec docs, "Wait will close the pipe after seeing the command
	// exit... it is thus incorrect to call Wait before all reads from the
	// pipe have completed." Wait() closes stdout's read end as part of its
	// cleanup once the child is reaped; if that races with the scanner's
	// in-flight Read, the read is torn down mid-call instead of draining
	// remaining buffered bytes and returning a clean EOF — silently
	// dropping already-written lines (observed as res.Err/res.ExitCode
	// staying zero-valued for a process that actually emitted them).
	// Waiting for readerDone first serializes "drain stdout" before
	// "reap+close", eliminating the race without risking a deadlock: the
	// reader always terminates (natural EOF, scan error, or ctx.Done), so
	// this can't block forever waiting to call Wait.
	waitCh := make(chan error, 1)
	go func() {
		<-readerDone
		waitCh <- cmd.Wait()
	}()

	select {
	case <-ctx.Done():
		// Graceful first (cancel frame + EOF), bounded by a group kill.
		writeLine([]byte(`{"v":1,"type":"cancel"}`))
		closeStdin()
		grace := KillGrace
		if opts.Access == AccessFull || opts.Access == AccessRead {
			// Under --tools none monomind doesn't read the cancel frame; a
			// full- or read-access turn (a dynamic-org worker) is stopped by
			// SIGTERM, which monomind turns into a kill of the agent's whole
			// process tree.
			terminateProcessGroup(cmd)
			grace = max(grace, FullAccessKillGrace)
		}
		killTimer := time.AfterFunc(grace, func() { killProcessGroup(cmd, cmd.Process.Pid) })
		defer killTimer.Stop()
		<-loopDone
		<-waitCh
		toolWg.Wait()
		if res.ExitCode == 0 && res.Err == nil {
			res.Err = &ProtocolError{Code: ErrCancelled, Message: "cancelled by caller", ExitCode: 130}
			res.ExitCode = 130
		}
		return res, nil

	case err := <-waitCh:
		// Process exited — drain buffered events, then finish up.
		<-readerDone
		<-loopDone
		toolWg.Wait()
		closeStdin()
		if res.ExitCode == 0 && res.Err == nil && err != nil {
			res.Err = &ProtocolError{Code: ErrRunnerError, Message: fmt.Sprintf("monomind exited: %v", err), ExitCode: 1}
			res.ExitCode = 1
		}
		if fallbackActive && sandboxEffective == SandboxStatusUnsupported && res.Err != nil && res.Err.Code == ErrUnsupported {
			res.Err.Message = fmt.Sprintf("the %s runtime has no %s sandbox and the sandbox fallback setting is 'fail' (change it in Settings to run without one): %s", opts.Runtime, opts.Sandbox, res.Err.Message)
		}
		return res, nil
	}
}

// formatDuration renders a duration in the protocol's duration syntax
// (sub-second is ms-suffixed; everything else whole seconds).
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%ds", int((d+time.Second/2)/time.Second))
}

// Scan proxies `monomind agent scan --json` (protocol §6). Scanning always
// exits 0 on the monomind side; errors here mean monomind is missing/broken.
//
// Handshakes via Ensure() first: an installed-but-too-old monomind answers
// an unrecognized `agent scan --json` invocation with human help text
// instead of JSON (exit 0), which would otherwise surface as a confusing
// "unparseable output" JSON error instead of the actionable "monomind X is
// too old" message every other entry point already gives.
func Scan(ctx context.Context) (*ScanResult, error) {
	bin, _, err := Ensure(ctx)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err := CommandContext(cctx, bin, "agent", "scan", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("agent scan failed: %w", err)
	}
	var res ScanResult
	if err := json.Unmarshal(JSONBody(out), &res); err != nil {
		return nil, fmt.Errorf("agent scan: unparseable output: %w", err)
	}
	// freebuff has no headless transport in monomind; never list it.
	kept := res.Agents[:0]
	for _, a := range res.Agents {
		if a.ID != "freebuff" {
			kept = append(kept, a)
		}
	}
	res.Agents = kept
	return &res, nil
}
