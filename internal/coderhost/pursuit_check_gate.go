package coderhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/missionpursuit/inputs"
	"github.com/monoes/mono-agent/internal/missionpursuit/verifier"
)

// When a goal check may start, and why the pursuit waits because of one. Every condition is read by the host from its own
// rows and the project folder, never from anything the lead wrote.

// checkSettled is the settled-work test of the check trigger: no workable task, no blocked task and at least one task that is
// not an epic (the same facts the close wake uses; the unmerged-branch half is checkMergesDone).
func checkSettled(t inputs.TaskCounts) bool {
	return t.Workable == 0 && t.Blocked == 0 && t.NonEpic >= 1
}

// pursuitCheckWaiting is the waiting reason the goal checks add to the gate, or "": only when the checks are set up, so a host
// without them is exactly the loop of S5b. A running check and a check that waits for the person's answers stop everything that
// spends (no epoch, no judge call, no wake-up); so does a reviewer that failed three checks in a row, a pin that is not
// independent and a project folder that cannot be used (once the mission is settled and a check would be next).
func (h *Host) pursuitCheckWaiting(ctx context.Context, m pursuitMission, st pursuitState, snap inputs.Snapshot) string {
	if !h.verifierReady() {
		return ""
	}
	f, err := missionpursuit.ReadCheckFacts(ctx, h.Store.db, m.ID, st.BaseCheckID)
	switch {
	case err != nil:
		return pursuitWaitingMeter // the checks cannot be read, so their spend cannot be measured
	case f.Running:
		return pursuitWaitingCheck
	case f.AwaitingPerson:
		return pursuitWaitingAnswer
	case checkReviewerFailing(f, st.PersonAt):
		return pursuitWaitingVerifier
	}
	if !checkSettled(snap.Tasks) {
		return ""
	}
	switch {
	case h.checkPinWhy(m) != "":
		return pursuitWaitingVerifierPin
	case h.checkerBroken() != "":
		return pursuitWaitingCannotCheck
	}
	if _, why := h.checkSubject(m); why != "" {
		return pursuitWaitingCannotCheck
	}
	return ""
}

// checkReviewerFailing: the last MaxBadReviewerRuns checks in a row could not use the reviewer, and the person has not acted since
// the latest of them. It counts across away windows (a window holds only two checks, so a count inside one could never reach
// three); the person's action lifts it.
func checkReviewerFailing(f missionpursuit.CheckFacts, personAt time.Time) bool {
	return f.BadReviewer >= missionpursuit.MaxBadReviewerRuns && !personAt.After(f.LatestStarted)
}

// checkPinWhy says why the checker may not check this mission, or "": the judge, the checker and the lead must be known and
// pairwise different (verifier.Independent), the lead's read now.
func (h *Host) checkPinWhy(m pursuitMission) string {
	if !h.verifierReady() {
		return "the goal checks are not set up"
	}
	if ok, why := verifier.Independent(h.Pursuit.Model.Pin(), h.Pursuit.Verifier.Pin, h.Pursuit.LeadPin(m.Profile, m.Conv)); !ok {
		return why
	}
	return ""
}

// checkSubject is the folder the check runs against: the lead's conversation folder as the host's own row has it (not text
// the lead names), resolved through symbolic links, and refused when it is not a folder, is "/", or is, contains or lies
// inside one of the folders the deployment forbids (the person's home folder and the host's state folder).
func (h *Host) checkSubject(m pursuitMission) (root, why string) {
	cwd, _, ok, err := h.Store.ConversationInfo(m.Profile, m.Conv)
	if err != nil || !ok || strings.TrimSpace(cwd) == "" {
		return "", "bad-subject: the lead's chat has no project folder"
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(cwd))
	if err != nil || !filepath.IsAbs(real) {
		return "", "bad-subject: the project folder does not resolve"
	}
	if st, err := os.Stat(real); err != nil || !st.IsDir() {
		return "", "bad-subject: the project folder is not a folder"
	}
	if real == string(filepath.Separator) || real == "." {
		return "", "bad-subject: the project folder is the root of the computer"
	}
	if why := SubjectForbidden(real, h.Pursuit.Verifier.Forbidden); why != "" {
		return "", why
	}
	return real, ""
}

// checkMergesDone says whether every settled non-epic task's latest writer branch has been merged (the mission close refuses
// on the same list, dynorg's mergesRemaining; this is its SQL form over the same artifacts: the newest branch of a task, and a
// merge of that ref recorded after it). An unreadable answer is "not done": nothing is spent on a guess.
func (s *Store) checkMergesDone(session int64) bool {
	var n int
	err := s.db.QueryRow(`WITH lb AS (SELECT task_id, MAX(id) AS id FROM task_artifacts WHERE kind = 'branch' GROUP BY task_id)
		SELECT COUNT(*) FROM tasks t JOIN lb ON lb.task_id = t.id JOIN task_artifacts b ON b.id = lb.id
		WHERE t.session_id = ? AND t.kind <> 'epic' AND (t.status = 'done' OR (t.status = 'review' AND t.verified = 1))
		AND NOT EXISTS (SELECT 1 FROM task_artifacts mg WHERE mg.task_id = t.id AND mg.kind = 'merge' AND mg.ref = b.ref AND mg.id > b.id)`, session).Scan(&n)
	return err == nil && n == 0
}

// checkLeadBusy says why a check must wait for the lead and its helpers: a chat of the mission is in a turn, or the lead has a
// message queued. A check spends no lead time, but it must not read a tree that is being written.
func (h *Host) checkLeadBusy(m pursuitMission) string {
	lead, found, err := h.Store.GetSession(m.Profile, m.Conv)
	switch {
	case err != nil || !found:
		return "the lead's session does not read"
	case lead.State != StateIdle || lead.TurnID != "" || h.Store.PendingFor(m.Profile, m.Conv) > 0:
		return "the lead is not free"
	}
	tree, err := h.Store.missionTree(m)
	if err != nil {
		return "the mission's chats do not read"
	}
	for _, conv := range tree {
		if conv == m.Conv {
			continue
		}
		c, found, err := h.Store.GetSession(m.Profile, conv)
		if err != nil {
			return "a chat of the mission does not read"
		}
		if found && (c.State != StateIdle || c.TurnID != "") {
			return "a chat of the mission is working"
		}
	}
	return ""
}

// The memory of the check step, lost on restart (which only costs one more quiet wait and one more probe).
type checkState struct {
	checkMu      sync.Mutex
	checkWG      sync.WaitGroup
	checkSlot    atomic.Bool // one check, or the preparation of one, at a time host-wide
	checkRuns    map[int64]*checkRun
	checkQuiet   map[int64]*checkQuiet
	checkNext    map[int64]time.Time // the earliest the next preparation of a mission may start
	checkTried   map[int64]bool      // a check was started for the mission since this host started
	checkSeen    map[int64]seenSum   // the stat manifest hash at the last full digest of the mission (a cheap change signal)
	checkBig     map[int64]bool      // the project is over maxManifestEntries (logged once)
	checkerOnce  sync.Once
	hostStart    time.Time
	verifierWhy  string // why the checker could not be built, "" when it could
	verifierCore verifier.Verifier
}

type checkRun struct {
	cancel context.CancelFunc
	id     int64
}

type checkQuiet struct {
	root, manifest string
	since          time.Time
}

// A folder with more entries than this is not looked at for quiet (its manifest would be a huge string every 30 seconds while the
// one check slot is held): such a mission is never checked, and the host log says so once.
var maxManifestEntries = 200000 // a variable only so a test can lower it

const manifestTimeout = time.Minute

func (h *Host) noteBigProject(session int64) {
	h.checkMu.Lock()
	first := !h.checkBig[session]
	if h.checkBig == nil {
		h.checkBig = map[int64]bool{}
	}
	h.checkBig[session] = true
	h.checkMu.Unlock()
	if first {
		h.logf("coder host: goal check of session %d: the project has more than %d files, so it is not checked (too-many-files)", session, maxManifestEntries)
	}
}

// checkPrepDue says whether a preparation of this mission's check may start now, and books the next one: a tree is looked at
// at most every PrepEvery (30 s) per mission.
func (h *Host) checkPrepDue(session int64, now time.Time) bool {
	h.checkMu.Lock()
	defer h.checkMu.Unlock()
	if next := h.checkNext[session]; now.Before(next) {
		return false
	}
	if h.checkNext == nil {
		h.checkNext = map[int64]time.Time{}
	}
	h.checkNext[session] = now.Add(h.Pursuit.Verifier.prepEvery())
	return true
}

// treeQuiet says whether nothing in the folder has been written for QuietFor, by comparing its stat manifest with the one
// remembered from the last look. The first look, a changed manifest or a changed folder starts the wait again.
func (h *Host) treeQuiet(ctx context.Context, session int64, root string, now time.Time) (quiet bool, sum string) {
	mctx, cancel := context.WithTimeout(ctx, manifestTimeout)
	manifest, err := finish.StatManifestCtx(mctx, root, maxManifestEntries)
	cancel()
	if err != nil {
		if errors.Is(err, finish.ErrTooManyFiles) {
			h.noteBigProject(session)
		}
		return false, ""
	}
	h.checkMu.Lock()
	defer h.checkMu.Unlock()
	hash := sha256.Sum256([]byte(manifest))
	sum = hex.EncodeToString(hash[:])
	q := h.checkQuiet[session]
	if q == nil || q.root != root || q.manifest != manifest {
		if h.checkQuiet == nil {
			h.checkQuiet = map[int64]*checkQuiet{}
		}
		h.checkQuiet[session] = &checkQuiet{root: root, manifest: manifest, since: now}
		return false, sum
	}
	return now.Sub(q.since) >= h.Pursuit.Verifier.quietFor(), sum
}
