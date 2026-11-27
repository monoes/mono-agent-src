package orgchat

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeClient is an org in memory: resolving an item flips it, so a second
// resolve sees it done, as monomind would.
type fakeClient struct {
	mu        sync.Mutex
	status    string
	questions []Question
	approvals []Approval
	gates     []Gate
	sent      []string
}

func (f *fakeClient) Status(context.Context, string, string) (json.RawMessage, error) {
	return json.Marshal(map[string]string{"status": f.status})
}

func (f *fakeClient) All(_ context.Context, _, _, kind string) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch kind {
	case "questions":
		return json.Marshal(map[string]interface{}{"items": f.questions})
	case "approvals":
		return json.Marshal(map[string]interface{}{"items": f.approvals})
	default:
		return json.Marshal(map[string]interface{}{"items": f.gates})
	}
}

func (f *fakeClient) Answer(_ context.Context, _, _, id, answer string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.questions {
		if f.questions[i].QuestionID == id {
			f.questions[i].Answer = &answer
		}
	}
	f.sent = append(f.sent, "answer "+id+" "+answer)
	return nil
}

func (f *fakeClient) Dismiss(_ context.Context, _, _, id, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.questions {
		if f.questions[i].QuestionID == id {
			f.questions[i].State = StateDismissed
		}
	}
	f.sent = append(f.sent, "dismiss "+id+" "+reason)
	return nil
}

func (f *fakeClient) Approve(_ context.Context, _, _, role, action string, approve bool, requestID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.approvals {
		a := &f.approvals[i]
		named := requestID == "" || (a.RequestID != nil && *a.RequestID == requestID)
		if a.RoleID == role && a.Action == action && a.Approved == nil && named {
			v := approve
			a.Approved = &v
		}
	}
	verb := "deny"
	if approve {
		verb = "approve"
	}
	f.sent = append(f.sent, verb+" "+role+" "+action+" "+requestID)
	return nil
}

func (f *fakeClient) Gate(_ context.Context, _, _, id string, approve bool, note string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.gates {
		if f.gates[i].ID == id {
			f.gates[i].Status = map[bool]string{true: "approved", false: "rejected"}[approve]
		}
	}
	f.sent = append(f.sent, "gate "+id+" "+note)
	return nil
}

func newFake() *fakeClient {
	req := "req-7"
	return &fakeClient{
		status: "running",
		questions: []Question{
			{QuestionID: "q-1", Role: "ceo", Question: "Which version?", TS: 1},
		},
		approvals: []Approval{
			{RoleID: "dev", Action: "Bash", TS: 5},
			{RoleID: "dev", Action: "WebFetch", TS: 6, RequestID: &req},
		},
		gates: []Gate{{ID: "gate-1", Name: "publish", RoleID: "ceo", Status: "pending", CreatedAt: 9}},
	}
}

func TestResolveAnswerIsIdempotent(t *testing.T) {
	f := newFake()
	ctx := context.Background()
	res, err := Resolve(ctx, f, t.TempDir(), Request{Org: "acme", Ref: "q-1", Answer: true, Text: "1.2.0"})
	if err != nil || res.Already || res.State != StateAnswered || res.Kind != KindQuestion {
		t.Fatalf("first answer = %+v, %v", res, err)
	}
	res, err = Resolve(ctx, f, t.TempDir(), Request{Org: "acme", Ref: "q-1", Answer: true, Text: "1.2.0"})
	if err != nil || !res.Already || res.State != StateAnswered {
		t.Fatalf("second answer = %+v, %v", res, err)
	}
	if len(f.sent) != 1 {
		t.Fatalf("sent %v, want one answer", f.sent)
	}
}

func TestResolveConcurrentClicksSendOnce(t *testing.T) {
	f := newFake()
	root := t.TempDir()
	var wg sync.WaitGroup
	results := make([]*Result, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := Resolve(context.Background(), f, root, Request{Org: "acme", Ref: "gate-1", Approve: true})
			if err != nil {
				t.Error(err)
			}
			results[i] = res
		}(i)
	}
	wg.Wait()
	if len(f.sent) != 1 {
		t.Fatalf("sent %v, want exactly one gate approval", f.sent)
	}
	fresh := 0
	for _, r := range results {
		if r != nil && !r.Already {
			fresh++
		}
		if r != nil && r.State != StateApproved {
			t.Errorf("state = %q", r.State)
		}
	}
	if fresh != 1 {
		t.Fatalf("%d results claim to have sent", fresh)
	}
}

func TestResolveRefusesAStoppedOrg(t *testing.T) {
	f := newFake()
	f.status = "stopped"
	_, err := Resolve(context.Background(), f, t.TempDir(), Request{Org: "acme", Ref: "req-7", Approve: true})
	var nr *NotRunningError
	if !errors.As(err, &nr) || nr.Status != "stopped" {
		t.Fatalf("err = %v, want NotRunningError(stopped)", err)
	}
	if len(f.sent) != 0 {
		t.Fatalf("sent %v to a stopped org", f.sent)
	}
	// An item resolved before the org stopped still answers "already".
	v := false
	f.approvals[1].Approved = &v
	res, err := Resolve(context.Background(), f, t.TempDir(), Request{Org: "acme", Ref: "req-7", Approve: true})
	if err != nil || !res.Already || res.State != StateDenied {
		t.Fatalf("resolved-then-stopped = %+v, %v", res, err)
	}
}

func TestResolveApprovalRefs(t *testing.T) {
	f := newFake()
	ctx := context.Background()
	res, err := Resolve(ctx, f, t.TempDir(), Request{Org: "acme", Ref: "req-7", Approve: false})
	if err != nil || res.State != StateDenied || res.Ref != "req-7" {
		t.Fatalf("by request id = %+v, %v", res, err)
	}
	res, err = Resolve(ctx, f, t.TempDir(), Request{Org: "acme", Ref: "dev:Bash:5", Approve: false})
	if err != nil || res.State != StateDenied || res.Ref != "dev:Bash:5" {
		t.Fatalf("by role:action:ts = %+v, %v", res, err)
	}
	want := []string{"deny dev WebFetch req-7", "deny dev Bash "}
	if len(f.sent) != 2 || f.sent[0] != want[0] || f.sent[1] != want[1] {
		t.Fatalf("sent %q, want %q", f.sent, want)
	}
}

func TestResolveNotFoundAndEmptyAnswer(t *testing.T) {
	f := newFake()
	ctx := context.Background()
	if _, err := Resolve(ctx, f, t.TempDir(), Request{Org: "acme", Ref: "q-missing", Answer: true, Text: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing question: %v", err)
	}
	if _, err := Resolve(ctx, f, t.TempDir(), Request{Org: "acme", Ref: "gate-missing", Approve: true}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing gate: %v", err)
	}
	if _, err := Resolve(ctx, f, t.TempDir(), Request{Org: "acme", Ref: "q-1", Answer: true, Text: "  "}); err == nil {
		t.Error("an empty answer was accepted")
	}
	if len(f.sent) != 0 {
		t.Fatalf("sent %v", f.sent)
	}
}

func TestLockWaitEndsWithTheContext(t *testing.T) {
	root := t.TempDir()
	unlock, err := lockOrg(context.Background(), root, "acme")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = Resolve(ctx, newFake(), root, Request{Org: "acme", Ref: "gate-1", Approve: true})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Fatalf("waiting on a held lock: %v after %s", err, time.Since(start))
	}
	if info, err := os.Stat(filepath.Join(root, ".monomind", "locks")); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("lock dir: %v %v", info, err)
	}
}

// Never two holders at once, however many contend (the claim file this
// replaced let two waiters both break a stale claim).
func TestLockHasOneHolderAtATime(t *testing.T) {
	root := t.TempDir()
	var mu sync.Mutex
	holders, most := 0, 0
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				release, err := lockOrg(context.Background(), root, "acme")
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				holders++
				if holders > most {
					most = holders
				}
				mu.Unlock()
				time.Sleep(time.Millisecond)
				mu.Lock()
				holders--
				mu.Unlock()
				release()
			}
		}()
	}
	wg.Wait()
	if most != 1 {
		t.Fatalf("%d holders at once", most)
	}
}

func TestResolveDismissedQuestionIsAlreadyDone(t *testing.T) {
	f := newFake()
	f.questions[0].State = StateDismissed
	res, err := Resolve(context.Background(), f, t.TempDir(), Request{Org: "acme", Ref: "q-1", Answer: true, Text: "1.2.0"})
	if err != nil || !res.Already || res.State != StateDismissed {
		t.Fatalf("answer on a dismissed question = %+v, %v", res, err)
	}
	if len(f.sent) != 0 {
		t.Fatalf("sent %v, want nothing for a dismissed question", f.sent)
	}
}

func TestResolveDismissIsIdempotent(t *testing.T) {
	f := newFake()
	ctx := context.Background()
	root := t.TempDir()
	res, err := Resolve(ctx, f, root, Request{Org: "acme", Ref: "q-1", Dismiss: true, Text: " moot "})
	if err != nil || res.Already || res.State != StateDismissed || res.Kind != KindQuestion {
		t.Fatalf("first dismiss = %+v, %v", res, err)
	}
	res, err = Resolve(ctx, f, root, Request{Org: "acme", Ref: "q-1", Dismiss: true})
	if err != nil || !res.Already || res.State != StateDismissed {
		t.Fatalf("second dismiss = %+v, %v", res, err)
	}
	// An answer after a dismissal reports the dismissal and sends nothing.
	res, err = Resolve(ctx, f, root, Request{Org: "acme", Ref: "q-1", Answer: true, Text: "late"})
	if err != nil || !res.Already || res.State != StateDismissed {
		t.Fatalf("answer after dismiss = %+v, %v", res, err)
	}
	if len(f.sent) != 1 || f.sent[0] != "dismiss q-1 moot" {
		t.Fatalf("sent %v, want one trimmed dismiss", f.sent)
	}
}

func TestResolveDismissAnsweredIsAlready(t *testing.T) {
	f := newFake()
	ans := "yes"
	f.questions[0].Answer = &ans
	res, err := Resolve(context.Background(), f, t.TempDir(), Request{Org: "acme", Ref: "q-1", Dismiss: true})
	if err != nil || !res.Already || res.State != StateAnswered || len(f.sent) != 0 {
		t.Fatalf("dismiss of answered = %+v, %v, sent %v", res, err, f.sent)
	}
}

func TestResolveDismissNotFound(t *testing.T) {
	f := newFake()
	if _, err := Resolve(context.Background(), f, t.TempDir(), Request{Org: "acme", Ref: "q-nope", Dismiss: true}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown question: %v, want ErrNotFound", err)
	}
}

// A stopped org still takes a dismissal (monomind queues the note), but
// not an answer.
func TestResolveDismissWorksOnAStoppedOrg(t *testing.T) {
	f := newFake()
	f.status = "stopped"
	res, err := Resolve(context.Background(), f, t.TempDir(), Request{Org: "acme", Ref: "q-1", Dismiss: true, Text: "stale"})
	if err != nil || res.Already || res.State != StateDismissed {
		t.Fatalf("dismiss on a stopped org = %+v, %v", res, err)
	}
	f2 := newFake()
	f2.status = "stopped"
	_, err = Resolve(context.Background(), f2, t.TempDir(), Request{Org: "acme", Ref: "q-1", Answer: true, Text: "1.2.0"})
	var nr *NotRunningError
	if !errors.As(err, &nr) {
		t.Fatalf("answer on a stopped org: err = %v, want NotRunningError", err)
	}
}
