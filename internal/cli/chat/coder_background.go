package chat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
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

// startComparable is true when two start values are made the same way (a hashed boot id of the same
// length before the colon); one of another form is not evidence that the pid was reused.
func startComparable(a, b string) bool {
	i, j := strings.IndexByte(a, ':'), strings.IndexByte(b, ':')
	return i > 0 && i == j
}

// procStateLetter is the state letter of /proc/<pid>/stat ('Z' for a zombie), 0 when unknown.
func procStateLetter(pid int) byte {
	if runtime.GOOS != "linux" || pid <= 0 {
		return 0
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	k := bytes.LastIndexByte(stat, ')')
	if k < 0 || k+2 >= len(stat) {
		return 0
	}
	return stat[k+2]
}

// parentOf is the parent pid of pid (Linux; 0 when unknown).
func parentOf(pid int) int {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	k := bytes.LastIndexByte(stat, ')')
	if k < 0 {
		return 0
	}
	f := strings.Fields(string(stat[k+1:]))
	if len(f) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(f[1])
	return n
}

// descendantOf says pid is below ancestor in the process tree (a few levels: the runner is started
// through at most a launcher or two).
func descendantOf(pid, ancestor int) bool {
	if runtime.GOOS != "linux" {
		return false
	}
	for i := 0; i < 8 && pid > 1; i++ {
		pid = parentOf(pid)
		if pid == ancestor {
			return true
		}
	}
	return false
}

// statStartTime is the starttime (field 22) of a /proc/<pid>/stat line; "" when it is not there.
// comm may hold spaces and parentheses, so the fields are counted from the last ')'.
func statStartTime(stat string) string {
	k := strings.LastIndexByte(stat, ')')
	if k < 0 {
		return ""
	}
	fields := strings.Fields(stat[k+1:])
	if len(fields) < 20 { // state is field 3, starttime field 22
		return ""
	}
	return fields[19]
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
	ticks := statStartTime(string(stat))
	if ticks == "" {
		return ""
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(string(boot))))
	return hex.EncodeToString(sum[:6]) + ":" + ticks
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

// stopBackground stops each recorded process that is still the turn's own: SIGTERM, then SIGKILL if
// it outlives grace. A process is pinned first (a pidfd on Linux), what identifies it is read from the
// pinned process, and every signal goes through the pin, so a pid that is reused meanwhile is never
// signalled. A process is the turn's own only if the marker it carries is the one the host read from
// the turn's runtime at the start of the turn (Root), is the marker recorded for it, and still is. Where
// a process cannot be pinned, or the turn's marker was not read, it is not stopped.
func stopBackground(refs []chatevents.ProcessRef, grace time.Duration) backgroundStop {
	res := backgroundStop{Stopped: []int{}, Gone: []int{}, Refused: []int{}}
	type target struct {
		ref chatevents.ProcessRef
		h   pinnedProcess
	}
	var signalled []target
	defer func() {
		for _, t := range signalled {
			t.h.close()
		}
	}()
	refuse := func(r chatevents.ProcessRef, reason string) {
		res.Refused = append(res.Refused, r.Pid)
		res.Survivors = append(res.Survivors, survivor{Pid: r.Pid, Command: shortCommand(r.Pid), Reason: reason})
	}
	for _, r := range refs {
		h, err := pinProcess(r.Pid)
		if errors.Is(err, errNoProcess) {
			res.Gone = append(res.Gone, r.Pid)
			continue
		}
		if err != nil {
			refuse(r, "it cannot be stopped safely here: "+err.Error())
			continue
		}
		verdict, reason := judgeRef(r, h)
		switch verdict {
		case refGone:
			res.Gone = append(res.Gone, r.Pid)
			h.close()
		case refRefused:
			refuse(r, reason)
			h.close()
		default:
			if err := h.signal(syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
				refuse(r, "it could not be signalled: "+err.Error())
				h.close()
				continue
			}
			signalled = append(signalled, target{r, h})
		}
	}
	deadline := time.Now().Add(grace)
	for _, t := range signalled {
		for t.h.alive() && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if t.h.alive() {
			// A handle that does not pin the pid has to be checked again right before the signal.
			if !t.h.pinsPid() {
				if v, _ := judgeRef(t.ref, t.h); v != refStop {
					if v == refRefused {
						refuse(t.ref, "it could not be shown to be the turn's own again before SIGKILL")
					} else {
						res.Gone = append(res.Gone, t.ref.Pid)
					}
					continue
				}
			}
			_ = t.h.signal(syscall.SIGKILL)
			for end := time.Now().Add(time.Second); t.h.alive() && time.Now().Before(end); {
				time.Sleep(50 * time.Millisecond)
			}
		}
		if t.h.alive() {
			res.Survivors = append(res.Survivors, survivor{Pid: t.ref.Pid, Command: shortCommand(t.ref.Pid), Reason: "it outlived SIGKILL"})
			continue
		}
		res.Stopped = append(res.Stopped, t.ref.Pid)
	}
	return res
}

type refVerdict int

const (
	refStop    refVerdict = iota // the turn's own process: stop it
	refGone                      // not running any more, or the pid belongs to another process now
	refRefused                   // cannot be shown to be the turn's own: not signalled, reported
)

// judgeRef reads what identifies the pinned process and compares it with the record. The reads come
// first and the liveness check after, so the reads provably belong to the pinned process.
func judgeRef(r chatevents.ProcessRef, h pinnedProcess) (refVerdict, string) {
	now := processIdentity(r.Pid)
	start := processStart(r.Pid)
	state := procStateLetter(r.Pid)
	if !h.alive() || state == 'Z' {
		// A zombie has ended: it runs nothing and only waits to be reaped.
		return refGone, ""
	}
	if r.Start != "" && start != "" && startComparable(r.Start, start) && start != r.Start {
		// Another process holds the pid (its start time is not the recorded one, and a process cannot
		// change that): the one the turn started is gone and this record is stale. Nothing is signalled.
		return refGone, ""
	}
	switch {
	case r.Root == "":
		return refRefused, "the turn's own process-tree marker was not read when it ran, so this process cannot be shown to be the turn's"
	case r.Identity == "":
		return refRefused, "its identity was not recorded, so it cannot be told from another process that reused the pid"
	case r.Identity != r.Root:
		return refRefused, "it carries a marker other than the turn's own"
	case now != r.Identity:
		return refRefused, "its pid now belongs to a process that cannot be shown to be the one the turn started"
	}
	return refStop, ""
}

// childrenOf is the pids whose parent is pid (Linux; nil elsewhere).
func childrenOf(pid int) []int {
	if runtime.GOOS != "linux" {
		return nil
	}
	// The kernel's own list, when it keeps one (CONFIG_PROC_CHILDREN).
	if tasks, err := os.ReadDir("/proc/" + strconv.Itoa(pid) + "/task"); err == nil {
		var kids []int
		ok := false
		for _, t := range tasks {
			b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/task/" + t.Name() + "/children")
			if err != nil {
				continue
			}
			ok = true
			for _, f := range strings.Fields(string(b)) {
				if n, err := strconv.Atoi(f); err == nil {
					kids = append(kids, n)
				}
			}
		}
		if ok {
			return kids
		}
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var kids []int
	for _, e := range entries {
		n, err := strconv.Atoi(e.Name())
		if err == nil && n > 0 && parentOf(n) == pid {
			kids = append(kids, n)
		}
	}
	return kids
}

// markerPoll is how long the host looks for the turn's marker after the runner starts.
var markerPoll = 10 * time.Second

// learnTurnMarker reads the exec-tree marker of a turn's own process tree from the runner's direct
// child (the runtime's process, which monomind starts with the marker in its environment) while the
// turn starts. It is the host's own reading, never the model's or the runtime's report. "" when the
// child or its marker cannot be read, or the children disagree: the turn's processes are then not
// stopped.
func learnTurnMarker(ctx context.Context, runner int) string {
	// The runner is the one this process started (directly or through a launcher): a pid that is not
	// below this process is not read.
	if !descendantOf(runner, os.Getpid()) {
		return ""
	}
	deadline := time.Now().Add(markerPoll)
	last := ""
	for {
		seen := map[string]bool{}
		for _, kid := range childrenOf(runner) {
			if id := processIdentity(kid); strings.HasPrefix(id, "tree:") {
				seen[id] = true
			}
		}
		if len(seen) > 1 {
			return ""
		}
		cur := ""
		for id := range seen {
			cur = id
		}
		// One marker, seen on two scans in a row: a child that shows up beside it in between is
		// not missed by the first look.
		if cur != "" && cur == last {
			return cur
		}
		last = cur
		if ctx.Err() != nil || time.Now().After(deadline) {
			return ""
		}
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(60 * time.Millisecond):
		}
	}
}

// stopConversationBackground stops the processes the conversation's turns left running. The record
// is collected again after each round, so one a turn recorded meanwhile is stopped too (three
// rounds at most). With stopIfActive false it stops nothing while a turn is active (active is
// true): the caller's delete is refused then anyway.
func stopConversationBackground(store *ai.AIStore, profileID, conversationID string, stopIfActive bool) (backgroundStop, bool, error) {
	res, active, _, err := stopConversationBackgroundSeen(store, profileID, conversationID, stopIfActive)
	return res, active, err
}

// stopConversationBackgroundSeen is stopConversationBackground and the records it handled (with the
// command text cleared), for a caller that checks no other was recorded since.
func stopConversationBackgroundSeen(store *ai.AIStore, profileID, conversationID string, stopIfActive bool) (res backgroundStop, active bool, seen map[chatevents.ProcessRef]bool, err error) {
	res = backgroundStop{Stopped: []int{}, Gone: []int{}, Refused: []int{}}
	seen = map[chatevents.ProcessRef]bool{}
	for round := 0; round < 3; round++ {
		refs, act, err := conversationBackgroundRefs(store, profileID, conversationID)
		if err != nil {
			return res, false, seen, err
		}
		active = act
		if active && !stopIfActive {
			return res, true, seen, nil
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
			return res, active, seen, nil
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
		return res, active, seen, err
	}
	for _, r := range refs {
		c := r
		c.Command = ""
		if !seen[c] {
			res.Survivors = append(res.Survivors, survivor{Pid: r.Pid, Command: r.Command, Reason: "it was recorded while the conversation's other processes were being stopped"})
		}
	}
	return res, active, seen, nil
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
