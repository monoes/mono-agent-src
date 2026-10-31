//go:build linux

package chat

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/dynorg"
	"github.com/monoes/mono-agent/internal/procbind"
)

func childBoundRig(t *testing.T) (*boundRig, *childFix) {
	t.Helper()
	f := newChildFix(t)
	r := newBoundRigOn(t, f.db, "")
	r.l.child = f.mint
	r.l.bound = newBoundLaunch(f.db.DB, fakeBoundHost{"gen-1", true}, r.binder, r.store, f.mint)
	r.session("default", f.child, "t1")
	return r, f
}

// A mission child's bound turn gets its task token as a v2 mission token written in the same transaction as its session
// token: the right owner (the run), session, task, role and actor; both bound to the turn's process; two entries
// active; no legacy task token minted; and both are revoked by owner when the run ends.
func TestABoundMissionChildGetsAV2TaskTokenBoundToItsTask(t *testing.T) {
	r, f := childBoundRig(t)
	turn, err := r.l.Start("default", f.child, "t1", "work", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	fr, err := procbind.ReadCredFrame(bytes.NewReader(r.waitFile(r.frame)))
	if err != nil || !strings.HasPrefix(string(fr.TaskToken), procbind.TaskPrefixV2) || !strings.HasPrefix(string(fr.SessionToken), procbind.SessionPrefixV2) || fr.MissionConfirmation != nil {
		t.Fatalf("frame: %v %v", fr, err)
	}
	task := string(fr.TaskToken)
	ctx := context.Background()
	var mid string
	if err := r.db.DB.QueryRow(`SELECT id FROM agent_tokens_v2`).Scan(&mid); err != nil {
		t.Fatal(err)
	}
	key, err := procbind.RowKeyIfExists(ctx, r.db.DB, "default")
	if err != nil {
		t.Fatal(err)
	}
	m, err := procbind.GetMissionToken(ctx, r.db.DB, key, "default", mid)
	owner := dynorg.TurnOwner("default", f.child, "t1")
	if err != nil || m.TokenHash != procbind.HashSecret(task) || m.Owner != owner || m.Session != f.sid || m.Task != f.ids[0] || m.Role != "worker" || m.Actor != dynorg.MissionWorkerName(f.sid, f.ids[0]) {
		t.Fatalf("mission token row: %+v %v", m, err)
	}
	b, err := procbind.GetBinding(ctx, r.db.DB, key, procbind.KindMission, "default", mid)
	if err != nil || b.WorkerPID != turn.PID() || b.Scope != procbind.MissionScope(f.sid, f.ids[0]) || b.HostGeneration != "gen-1" {
		t.Fatalf("mission binding: %+v %v", b, err)
	}
	if st := r.binder.Stats(); st.Active != 2 {
		t.Errorf("registry: %+v, want 2 active (session + task)", st)
	}
	var legacy int
	if err := r.db.DB.QueryRow(`SELECT COUNT(*) FROM agent_tokens WHERE mission_session > 0`).Scan(&legacy); err != nil || legacy != 0 {
		t.Errorf("legacy mission tokens: %d %v", legacy, err)
	}
	// the run ends: the task token is revoked by owner, and so is its registration
	if err := os.WriteFile(r.release, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = turn.Wait()
	if rv, err := procbind.IsRevoked(ctx, r.db.DB, procbind.KindMission, mid); err != nil || !rv {
		t.Errorf("the task token survived the run: %v %v", rv, err)
	}
	if l, ok := r.binder.Acquire(mid, "default", procbind.MissionScope(f.sid, f.ids[0])); ok {
		l.Release()
		t.Error("the task token's registration survived the run")
	}
}

// A child that ended or was replaced between the decision and the mint gets nothing: the attempt is re-read inside the
// transaction.
func TestABoundChildThatEndedBeforeTheMintGetsNoToken(t *testing.T) {
	r, f := childBoundRig(t)
	r.l.bound.beforeMint = func() {
		if _, err := coderhost.NewStore(f.db.DB).End("default", f.child, coderhost.EndStopped); err != nil {
			t.Error(err)
		}
	}
	_, err := r.l.Start("default", f.child, "t1", "work", nil, "")
	if err == nil || !strings.Contains(err.Error(), "ended or was replaced") {
		t.Fatalf("%v", err)
	}
	if tokens, bindings := r.v2Rows(); tokens != 0 || bindings != 0 {
		t.Errorf("rows were written: %d tokens, %d bindings", tokens, bindings)
	}
	var mission int
	if err := r.db.DB.QueryRow(`SELECT COUNT(*) FROM agent_tokens_v2`).Scan(&mission); err != nil || mission != 0 {
		t.Errorf("v2 mission tokens: %d %v", mission, err)
	}
	if st := r.binder.Stats(); st.Active != 0 || st.Pending != 0 {
		t.Errorf("registrations remain: %+v", st)
	}
	if raw, err := os.ReadFile(r.frame); err == nil && len(raw) > 0 {
		t.Error("a frame was delivered")
	}
}

func TestABoundChildCannotCarryAMissionConfirmation(t *testing.T) {
	r, f := childBoundRig(t)
	if _, err := r.l.Start("default", f.child, "t1", "work", nil, "12.nonceNONCEnonce"); err == nil || !strings.Contains(err.Error(), "cannot carry a mission confirmation") {
		t.Fatalf("%v", err)
	}
}

// The chat process takes a v2 task token out of the mission-confirmation slot exactly as it does a legacy one: it is
// judged as the child's task token (and found wrong here), never mistaken for a mission confirmation.
func TestTheChildGateRoutesAV2TaskTokenAsATaskToken(t *testing.T) {
	_, f := childBoundRig(t)
	_, err := childTaskGate(f.db.DB, "default", f.child, procbind.TaskPrefixV2+"nottherealsecret")
	if err == nil || strings.Contains(err.Error(), "cannot carry a mission confirmation") || !strings.Contains(err.Error(), "task token") {
		t.Fatalf("a v2 task token was not routed as one: %v", err)
	}
	_, err = childTaskGate(f.db.DB, "default", f.child, agenttokens.Prefix+"nottherealsecret")
	if err == nil || strings.Contains(err.Error(), "cannot carry a mission confirmation") {
		t.Fatalf("a legacy task token no longer routes as one: %v", err)
	}
}
