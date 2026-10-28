package chat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// execTreeEnv is the variable monomind puts on every process a full-access
// turn starts, set to a random per-turn token (monomind's
// process-tree-marker.ts).
const execTreeEnv = "MONOMIND_EXEC_TREE"

// processIdentity says what makes pid the process it is now, so a later
// stop can tell it from an unrelated process that reused the pid: its
// turn's MONOMIND_EXEC_TREE token on Linux, else its start time. "" when
// the process is gone or can't be identified.
func processIdentity(pid int) string {
	if pid <= 0 {
		return ""
	}
	if runtime.GOOS == "linux" {
		env, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
		if err != nil {
			return ""
		}
		for _, kv := range bytes.Split(env, []byte{0}) {
			if v, ok := strings.CutPrefix(string(kv), execTreeEnv+"="); ok && v != "" {
				return "tree:" + v
			}
		}
		return ""
	}
	if runtime.GOOS == "windows" {
		return ""
	}
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if s := strings.TrimSpace(string(out)); err == nil && s != "" {
		return "started:" + s
	}
	return ""
}

// processStart is the machine's boot id and the process's start time in clock ticks since boot
// (Linux): the same for one process for its whole life, whatever it does to its own environment
// or memory, and different for any other process that later gets the pid. "" where it cannot be
// read or the platform has no such value.
func processStart(pid int) string {
	if runtime.GOOS != "linux" || pid <= 0 {
		return ""
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	// pid (comm) state ppid ...: comm may hold spaces and parentheses, so count from the last ')'.
	rest := string(stat)
	k := strings.LastIndexByte(rest, ')')
	if k < 0 {
		return ""
	}
	fields := strings.Fields(rest[k+1:])
	if len(fields) < 20 { // state is field 3, starttime field 22
		return ""
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(boot)) + ":" + fields[19]
}

// shortCommand is pid's command line (clicore.ProcessCommand), shortened for a
// notice.
func shortCommand(pid int) string {
	cmd := clicore.ProcessCommand(pid)
	if len(cmd) > 120 {
		cmd = cmd[:117] + "..."
	}
	return cmd
}

// backgroundSettle is how long a turn's end waits for the processes its
// done event listed to exit before warning about them.
var backgroundSettle = 2 * time.Second

// stillRunning is the pids still alive once they had up to settle to exit.
func stillRunning(pids []int, settle time.Duration) []int {
	deadline := time.Now().Add(settle)
	for {
		var alive []int
		for _, pid := range pids {
			if processAlive(pid) {
				alive = append(alive, pid)
			}
		}
		if len(alive) == 0 || !time.Now().Before(deadline) {
			return alive
		}
		pids = alive
		time.Sleep(100 * time.Millisecond)
	}
}

// backgroundStop is `coder stop-background --json`.
type backgroundStop struct {
	Stopped []int `json:"stopped"`
	Gone    []int `json:"gone"`
	Refused []int `json:"refused"`
	// Survivors are the refused processes, and those that outlived SIGKILL,
	// with why each is still running.
	Survivors []survivor `json:"survivors,omitempty"`
}

// survivor is a recorded process that was not stopped and may still be alive.
type survivor struct {
	Pid     int    `json:"pid"`
	Command string `json:"command,omitempty"`
	Reason  string `json:"reason"`
}

// stopBackground stops each recorded process that is still the same
// process: SIGTERM, then SIGKILL if it outlives grace.
func stopBackground(refs []chatevents.ProcessRef, grace time.Duration) backgroundStop {
	res := backgroundStop{Stopped: []int{}, Gone: []int{}, Refused: []int{}}
	var signalled []int
	for _, r := range refs {
		now := processIdentity(r.Pid)
		switch {
		case now == "" && !processAlive(r.Pid):
			res.Gone = append(res.Gone, r.Pid)
		case r.Start != "" && processStart(r.Pid) != "" && processStart(r.Pid) != r.Start:
			// The pid now belongs to another process (its start time is not the recorded one, and a process
			// cannot change that): the one the turn started is gone, this record is stale and nothing is
			// signalled. Without a recorded start time, or when it cannot be read, an identity that does not
			// match stays a survivor: the process can change its own environment.
			res.Gone = append(res.Gone, r.Pid)
		case r.Identity == "" || now != r.Identity:
			res.Refused = append(res.Refused, r.Pid)
			reason := "its identity was not recorded, so it cannot be told from another process that reused the pid"
			if r.Identity != "" {
				reason = "its pid now belongs to a process that cannot be shown to be the one the turn started"
			}
			res.Survivors = append(res.Survivors, survivor{Pid: r.Pid, Command: shortCommand(r.Pid), Reason: reason})
		default:
			if err := terminateProcess(r.Pid); err != nil {
				res.Refused = append(res.Refused, r.Pid)
				res.Survivors = append(res.Survivors, survivor{Pid: r.Pid, Command: shortCommand(r.Pid), Reason: "it could not be signalled: " + err.Error()})
				continue
			}
			signalled = append(signalled, r.Pid)
		}
	}
	deadline := time.Now().Add(grace)
	for _, pid := range signalled {
		for processAlive(pid) && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if processAlive(pid) {
			_ = killProcess(pid)
		}
		if processAlive(pid) {
			res.Survivors = append(res.Survivors, survivor{Pid: pid, Command: shortCommand(pid), Reason: "it outlived SIGKILL"})
			continue
		}
		res.Stopped = append(res.Stopped, pid)
	}
	return res
}

// stopConversationBackground stops the processes the conversation's turns left running. The record
// is collected again after each round, so one a turn recorded meanwhile is stopped too (three
// rounds at most). With stopIfActive false it stops nothing while a turn is active (active is
// true): the caller's delete is refused then anyway.
func stopConversationBackground(store *ai.AIStore, profileID, conversationID string, stopIfActive bool) (res backgroundStop, active bool, err error) {
	res = backgroundStop{Stopped: []int{}, Gone: []int{}, Refused: []int{}}
	seen := map[chatevents.ProcessRef]bool{}
	for round := 0; round < 3; round++ {
		refs, act, err := conversationBackgroundRefs(store, profileID, conversationID)
		if err != nil {
			return res, false, err
		}
		active = act
		if active && !stopIfActive {
			return res, true, nil
		}
		var fresh []chatevents.ProcessRef
		for _, r := range refs {
			r.Command = ""
			if !seen[r] {
				seen[r] = true
				fresh = append(fresh, r)
			}
		}
		if len(fresh) == 0 {
			return res, active, nil
		}
		stop := stopBackground(fresh, 3*time.Second)
		res.Stopped = append(res.Stopped, stop.Stopped...)
		res.Gone = append(res.Gone, stop.Gone...)
		res.Refused = append(res.Refused, stop.Refused...)
		res.Survivors = append(res.Survivors, stop.Survivors...)
		afterBackgroundRound()
	}
	// A record made during the last round was not stopped: it keeps the caller from going on.
	refs, _, err := conversationBackgroundRefs(store, profileID, conversationID)
	if err != nil {
		return res, active, err
	}
	for _, r := range refs {
		c := r
		c.Command = ""
		if !seen[c] {
			res.Survivors = append(res.Survivors, survivor{Pid: r.Pid, Command: r.Command, Reason: "it was recorded while the conversation's other processes were being stopped"})
		}
	}
	return res, active, nil
}

// turnBackgroundRefs is every process the turn's coder.background notices
// recorded as left running.
func turnBackgroundRefs(store *ai.AIStore, profileID, conversationID, turnID string) ([]chatevents.ProcessRef, error) {
	var refs []chatevents.ProcessRef
	for after := int64(0); ; {
		page, err := store.GetEvents(conversationID, turnID, profileID, after, 1000)
		if err != nil {
			return nil, chatStoreErr(err)
		}
		for _, ev := range page {
			var n chatevents.NoticePayload
			if ev.Type == chatevents.EventNotice && json.Unmarshal(ev.Payload, &n) == nil && n.Code == noticeCoderBackground {
				refs = append(refs, n.Processes...)
			}
		}
		if len(page) < 1000 {
			return refs, nil
		}
		after = page[len(page)-1].Seq
	}
}

// afterBackgroundRound is a seam for tests: it runs after each round of stopping a conversation's
// background processes, before the record is collected again.
var afterBackgroundRound = func() {}

// conversationBackgroundRefs is every process any turn of the conversation
// recorded as left running, and whether one of its turns is still active.
func conversationBackgroundRefs(store *ai.AIStore, profileID, conversationID string) (refs []chatevents.ProcessRef, active bool, err error) {
	for cursor := ""; ; {
		turns, next, err := store.ListTurns(conversationID, profileID, cursor, 200)
		if err != nil {
			return nil, false, chatStoreErr(err)
		}
		for _, t := range turns {
			if t.Status == "active" {
				active = true
			}
			r, err := turnBackgroundRefs(store, profileID, conversationID, t.ID)
			if err != nil {
				return nil, false, err
			}
			refs = append(refs, r...)
		}
		if next == "" {
			return refs, active, nil
		}
		cursor = next
	}
}

func newCoderStopBackgroundCmd(cfg *Deps) *cobra.Command {
	var conversationID, turnID string
	cmd := &cobra.Command{
		Use:   "stop-background",
		Short: "Stop the background processes a coder turn left running",
		Long: "Stops the processes a coder turn reported still running when it ended (its " +
			"coder.background notice). A process is only stopped while it is still the one the turn " +
			"started; one whose pid now belongs to something else is refused.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if conversationID == "" || turnID == "" {
				return clicore.InvalidInput("--conversation and --turn are required")
			}
			store, profileID, closeStore, err := openChatHistory(cfg)
			if err != nil {
				return err
			}
			defer closeStore()
			refs, err := turnBackgroundRefs(store, profileID, conversationID, turnID)
			if err != nil {
				return err
			}
			res := stopBackground(refs, 3*time.Second)
			if cfg.JSONOutput() {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(res)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "stopped %v, already gone %v, refused %v\n", res.Stopped, res.Gone, res.Refused)
			return nil
		},
	}
	cmd.Flags().StringVar(&conversationID, "conversation", "", "The coder conversation")
	cmd.Flags().StringVar(&turnID, "turn", "", "The turn that left the processes running")
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}
