package ops

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is a virtual monotonic clock: Wait blocks until the test advances it.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []*waiter
}

type waiter struct {
	until time.Time
	ch    chan struct{}
}

func newClock() *fakeClock { return &fakeClock{now: time.Unix(1_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Wait(ctx context.Context, until time.Time) error {
	c.mu.Lock()
	if !c.now.Before(until) {
		c.mu.Unlock()
		return nil
	}
	w := &waiter{until, make(chan struct{})}
	c.waiters = append(c.waiters, w)
	c.mu.Unlock()
	select {
	case <-w.ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	keep := c.waiters[:0]
	for _, w := range c.waiters {
		if !c.now.Before(w.until) {
			close(w.ch)
		} else {
			keep = append(keep, w)
		}
	}
	c.waiters = keep
}

// waitFor blocks (real time, bounded) until some waiter waits for until.
func (c *fakeClock) waitFor(t *testing.T, until time.Time) {
	t.Helper()
	for i := 0; i < 5000; i++ {
		c.mu.Lock()
		for _, w := range c.waiters {
			if w.until.Equal(until) {
				c.mu.Unlock()
				return
			}
		}
		c.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("nobody waits for %v", until)
}

type fakeSecrets struct{ v string }

func (f fakeSecrets) Value(context.Context, string) ([]byte, error) { return []byte(f.v), nil }

type fakeTransport struct {
	do func(ctx context.Context, req VaultArgs, secret []byte) (Response, error)
}

func (f fakeTransport) Do(ctx context.Context, req VaultArgs, secret []byte) (Response, error) {
	return f.do(ctx, req, secret)
}

type auditLog struct {
	mu   sync.Mutex
	rows []AuditRow
}

func (a *auditLog) Append(_ context.Context, r AuditRow) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rows = append(a.rows, r)
	return nil
}

func (a *auditLog) results() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, r := range a.rows {
		out = append(out, r.Result)
	}
	return out
}

var testVault = &VaultUse{Secrets: []string{"S1"}, Hosts: []string{"example.com"}, MaxAttempts: 3, BucketSize: 10, BucketCount: 3, MaxCallMS: 1000, SlotMS: 100, Predicate: "MARK"}

func approvedVault(v *VaultUse) Authorization {
	return Authorization{op: OpVaultUse, hash: "h", id: "a1", vault: v}
}

var testReq = VaultArgs{Secret: "S1", Method: "GET", URL: "https://example.com/x", BodyHash: "b", Inject: "authorization"}

type vrig struct {
	t     *testing.T
	clk   *fakeClock
	audit *auditLog
	x     *VaultExecutor
	t0    time.Time
}

func newVRig(t *testing.T, tr Transport) *vrig {
	clk := newClock()
	au := &auditLog{}
	return &vrig{t: t, clk: clk, audit: au, t0: clk.Now(), x: &VaultExecutor{Clock: clk, Transport: tr, Secrets: fakeSecrets{"s3cret-value"}, Auditor: au}}
}

type result struct {
	env Envelope
	err error
}

func (r *vrig) start(v *VaultUse, ctx context.Context) <-chan result {
	ch := make(chan result, 1)
	go func() {
		e, err := r.x.Run(ctx, approvedVault(v), testReq)
		ch <- result{e, err}
	}()
	r.clk.waitFor(r.t, r.at(time.Duration(v.MaxCallMS)*time.Millisecond)) // Run has taken its start instant
	return ch
}

func (r *vrig) at(d time.Duration) time.Time { return r.t0.Add(d) }

func (r *vrig) finish(ch <-chan result, releaseAt time.Duration) result {
	r.t.Helper()
	if r.clk.Now().Before(r.at(releaseAt)) {
		r.clk.waitFor(r.t, r.at(releaseAt))
		select {
		case res := <-ch:
			r.t.Fatalf("delivered before its release instant %v: %+v", releaseAt, res)
		default:
		}
		r.clk.Advance(releaseAt - r.clk.Now().Sub(r.t0))
	}
	select {
	case res := <-ch:
		return res
	case <-time.After(5 * time.Second):
		r.t.Fatal("not delivered at its release instant")
		return result{}
	}
}

// pipeTransport answers with headers at once and a body the test feeds.
func pipeTransport(pr *io.PipeReader) Transport {
	return fakeTransport{func(context.Context, VaultArgs, []byte) (Response, error) {
		return Response{Status: 200, Body: pr}, nil
	}}
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

type endless struct{}

func (endless) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestLengthBucketsAndTheReadCap(t *testing.T) { // T5f7
	for _, c := range []struct {
		size, count int64
		body        int64 // -1: endless
		want        int
	}{
		{10, 3, 0, 0}, {10, 3, 1, 1}, {10, 3, 10, 1}, {10, 3, 11, 2}, {10, 3, 30, 3}, {10, 3, 31, 3}, {10, 3, -1, 3},
		{10, 1, 0, 0}, {10, 1, 1, 1}, {10, 1, 10, 1}, {10, 1, 11, 1}, {10, 1, -1, 1}, // T5f7b
	} {
		v := *testVault
		v.BucketSize, v.BucketCount = c.size, c.count
		var cr *countingReader
		var src io.Reader = endless{}
		if c.body >= 0 {
			src = io.LimitReader(endless{}, c.body)
		}
		cr = &countingReader{r: src}
		tr := fakeTransport{func(context.Context, VaultArgs, []byte) (Response, error) {
			return Response{Status: 200, Body: io.NopCloser(cr)}, nil
		}}
		r := newVRig(t, tr)
		res := r.finish(r.start(&v, context.Background()), 100*time.Millisecond)
		if res.err != nil || res.env.LengthBucket != c.want || res.env.Outcome != OutcomeOK {
			t.Fatalf("%+v: %+v %v", c, res.env, res.err)
		}
		if cr.n > v.Cap() {
			t.Fatalf("%+v: the host read %d bytes, over the cap %d", c, cr.n, v.Cap())
		}
	}
}

func TestTimingSlotsFollowTheCompletionOfTheBodyRead(t *testing.T) { // T5f10, T5f14
	for _, c := range []struct {
		arrive time.Duration
		slot   int
	}{{1, 1}, {99, 1}, {100, 1}, {101, 2}, {199, 2}, {200, 2}, {201, 3}} {
		pr, pw := io.Pipe()
		r := newVRig(t, pipeTransport(pr))
		ch := r.start(testVault, context.Background())
		r.clk.Advance(c.arrive * time.Millisecond)
		pw.Write([]byte("abc"))
		pw.Close()
		res := r.finish(ch, time.Duration(c.slot)*100*time.Millisecond)
		if res.err != nil || res.env.Outcome != OutcomeOK || res.env.LengthBucket != 1 {
			t.Fatalf("arrive %v: %+v %v", c.arrive, res.env, res.err)
		}
	}
}

// The P1 of note 13.4 (round 8): headers come at once, the body trickles in across slot boundaries and ends late.
// Arrival is the completion of the capped read, so the slot is the last one and the envelope is complete; releasing on
// the headers' slot would have to guess the length and the predicate.
func TestArrivalIsTheCompletionOfTheCappedBodyRead(t *testing.T) {
	pr, pw := io.Pipe()
	r := newVRig(t, pipeTransport(pr))
	ch := r.start(testVault, context.Background())
	r.clk.Advance(50 * time.Millisecond)
	pw.Write([]byte("0123456789ab")) // 12 bytes, in slot 1
	r.clk.Advance(120 * time.Millisecond)
	pw.Write([]byte("cdefghij")) // 20 bytes, in slot 2
	r.clk.Advance(120 * time.Millisecond)
	pw.Write([]byte("MA")) // 22 bytes, slot 3: the marker straddles the next chunk
	r.clk.Advance(300 * time.Millisecond)
	pw.Write([]byte("RK")) // 24 bytes, at 590 ms: slot 6
	pw.Close()
	res := r.finish(ch, 600*time.Millisecond)
	yes := true
	if res.err != nil || res.env.Outcome != OutcomeOK || res.env.LengthBucket != 3 || res.env.Predicate == nil || *res.env.Predicate != yes {
		t.Fatalf("%+v %v", res.env, res.err)
	}
}

func TestACallThatReachesMaxCallIsATimeoutInTheLastSlot(t *testing.T) { // T5f4, T5f9
	pr, _ := io.Pipe() // the body never ends
	r := newVRig(t, pipeTransport(pr))
	ch := r.start(testVault, context.Background())
	r.clk.waitFor(t, r.at(1000*time.Millisecond))
	r.clk.Advance(1000 * time.Millisecond)
	select {
	case res := <-ch:
		if res.err != nil || res.env != (Envelope{Outcome: OutcomeTimeout}) {
			t.Fatalf("%+v %v", res.env, res.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no timeout")
	}
}

// jump moves the clock without waking anyone, so the timeout signal does not fire and the arrival rule alone decides.
func (c *fakeClock) jump(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// noTimerClock never fires the wait for the call's deadline, so only the arrival rule can produce a timeout.
type noTimerClock struct {
	*fakeClock
	deadline time.Time
	used     *atomic.Bool
}

func (c noTimerClock) Wait(ctx context.Context, until time.Time) error {
	if until.Equal(c.deadline) && c.used.CompareAndSwap(false, true) {
		<-ctx.Done()
		return ctx.Err()
	}
	return c.fakeClock.Wait(ctx, until)
}

func TestABodyThatCompletesAtMaxCallIsATimeoutEvenWithoutTheTimerSignal(t *testing.T) { // T5f10
	var clk *fakeClock
	tr := fakeTransport{func(context.Context, VaultArgs, []byte) (Response, error) {
		clk.jump(1000 * time.Millisecond)
		return Response{Status: 200, Body: io.NopCloser(strings.NewReader("abc"))}, nil
	}}
	r := newVRig(t, tr)
	clk = r.clk
	r.x.Clock = noTimerClock{r.clk, r.at(1000 * time.Millisecond), new(atomic.Bool)}
	e, err := r.x.Run(context.Background(), approvedVault(testVault), testReq)
	res := result{e, err}
	if res.err != nil || res.env != (Envelope{Outcome: OutcomeTimeout}) {
		t.Fatalf("%+v %v", res.env, res.err)
	}
}

func TestLengthBucketSaturatesAtTheCount(t *testing.T) {
	for _, c := range [][4]int64{{0, 10, 3, 0}, {1, 10, 3, 1}, {30, 10, 3, 3}, {31, 10, 3, 3}, {1 << 40, 10, 3, 3}, {5, 10, 1, 1}, {11, 10, 1, 1}} {
		if got := LengthBucket(c[0], c[1], c[2]); got != c[3] {
			t.Fatalf("%v: %d", c, got)
		}
	}
}

func TestOperationsDoNotWaitForEachOther(t *testing.T) { // T5f11, T5f12
	prA, _ := io.Pipe()
	r := newVRig(t, pipeTransport(prA))
	a := r.start(testVault, context.Background())
	r.clk.waitFor(t, r.at(1000*time.Millisecond))
	// B starts later and answers in slot 1 of its own
	trB := fakeTransport{func(context.Context, VaultArgs, []byte) (Response, error) {
		return Response{Status: 204, Body: io.NopCloser(strings.NewReader(""))}, nil
	}}
	x2 := *r.x
	x2.Transport = trB
	b := make(chan result, 1)
	go func() { e, err := x2.Run(context.Background(), approvedVault(testVault), testReq); b <- result{e, err} }()
	r.clk.waitFor(t, r.at(100*time.Millisecond))
	r.clk.Advance(100 * time.Millisecond)
	select {
	case res := <-b:
		if res.env.Outcome != OutcomeOK || res.env.Status != 204 || res.env.LengthBucket != 0 {
			t.Fatalf("%+v", res.env)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("B was held behind A")
	}
	select {
	case <-a:
		t.Fatal("A answered early")
	default:
	}
}

func TestStopCancelsAnAttemptAndDeliversNothing(t *testing.T) { // T5f13
	pr, _ := io.Pipe()
	r := newVRig(t, pipeTransport(pr))
	ctx, stop := context.WithCancel(context.Background())
	ch := r.start(testVault, ctx)
	r.clk.waitFor(t, r.at(1000*time.Millisecond))
	stop()
	res := <-ch
	if !errors.Is(res.err, ErrCancelled) || res.env != (Envelope{}) {
		t.Fatalf("%+v %v", res.env, res.err)
	}
	if got := r.audit.results(); len(got) != 1 || got[0] != "vault.attempt:cancelled" {
		t.Fatalf("audit %v", got)
	}
	// cancelled after the body completed but before the release instant: still nothing delivered
	pr2, pw2 := io.Pipe()
	r2 := newVRig(t, pipeTransport(pr2))
	ctx2, stop2 := context.WithCancel(context.Background())
	ch2 := r2.start(testVault, ctx2)
	pw2.Write([]byte("x"))
	pw2.Close()
	r2.clk.waitFor(t, r2.at(100*time.Millisecond))
	stop2()
	if res := <-ch2; !errors.Is(res.err, ErrCancelled) {
		t.Fatalf("%+v %v", res.env, res.err)
	}
}

func TestReflectingDestinationsLeakOnlyTheEnvelope(t *testing.T) { // T5f, T5f2, T5f3, T5f15
	secret := "s3cret-value"
	b64 := base64.StdEncoding.EncodeToString([]byte(secret))
	pct := url.QueryEscape(secret + "/=")
	for name, tr := range map[string]Transport{
		"body":        bodyOf(secret),
		"base64":      bodyOf(b64),
		"percent":     bodyOf(pct),
		"malformed":   errTransport(errors.Join(ErrMalformedResponse, errors.New("bad bytes "+secret))),
		"reset":       errTransport(errors.New("read tcp: connection reset, banner " + secret)),
		"bad status":  statusTransport(9999),
		"echo-header": bodyOf("Content-Type: " + secret),
	} {
		r := newVRig(t, tr)
		res := r.finish(r.start(testVault, context.Background()), 100*time.Millisecond)
		raw, _ := json.Marshal(res.env)
		for _, leak := range []string{secret, b64, pct} {
			if strings.Contains(string(raw), leak) {
				t.Fatalf("%s: the envelope %s carries %q", name, raw, leak)
			}
		}
		switch name {
		case "malformed", "bad status":
			if res.env.Outcome != OutcomeMalformed {
				t.Fatalf("%s: %+v", name, res.env)
			}
		case "reset":
			if res.env.Outcome != OutcomeTransportError {
				t.Fatalf("%s: %+v", name, res.env)
			}
		}
		if res.env.Predicate != nil && name != "body" && name != "base64" && name != "percent" && name != "echo-header" {
			t.Fatalf("%s: a predicate without a body: %+v", name, res.env)
		}
	}
}

func bodyOf(s string) Transport {
	return fakeTransport{func(context.Context, VaultArgs, []byte) (Response, error) {
		return Response{Status: 200, Body: io.NopCloser(strings.NewReader(s))}, nil
	}}
}
func errTransport(err error) Transport {
	return fakeTransport{func(context.Context, VaultArgs, []byte) (Response, error) { return Response{}, err }}
}
func statusTransport(code int) Transport {
	return fakeTransport{func(context.Context, VaultArgs, []byte) (Response, error) {
		return Response{Status: code, Body: io.NopCloser(strings.NewReader("x"))}, nil
	}}
}

func TestTheEnvelopeHasNoOtherField(t *testing.T) { // T5f: the type-level test
	want := map[string]reflect.Kind{"Outcome": reflect.String, "Status": reflect.Int, "LengthBucket": reflect.Int, "Predicate": reflect.Ptr}
	typ := reflect.TypeOf(Envelope{})
	if typ.NumField() != len(want) {
		t.Fatalf("the envelope has %d fields", typ.NumField())
	}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if k, ok := want[f.Name]; !ok || k != f.Type.Kind() {
			t.Fatalf("unexpected field %s %s", f.Name, f.Type)
		}
	}
	if reflect.TypeOf(Envelope{}.Predicate).Elem().Kind() != reflect.Bool {
		t.Fatal("the predicate is not a boolean")
	}
}

func TestTheSecretIsInjectedAtTheTransportOnly(t *testing.T) { // T5f
	var got []byte
	var at string
	tr := fakeTransport{func(_ context.Context, req VaultArgs, secret []byte) (Response, error) {
		got, at = secret, req.Inject
		return Response{Status: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
	}}
	r := newVRig(t, tr)
	r.finish(r.start(testVault, context.Background()), 100*time.Millisecond)
	if string(got) != "s3cret-value" || at != "authorization" {
		t.Fatalf("%q at %q", got, at)
	}
}

func TestAZeroAuthorizationRunsNothing(t *testing.T) {
	called := false
	r := newVRig(t, fakeTransport{func(context.Context, VaultArgs, []byte) (Response, error) { called = true; return Response{}, nil }})
	env, err := r.x.Run(context.Background(), Authorization{}, testReq)
	if err != nil || env.Outcome != OutcomeRefused || called {
		t.Fatalf("%+v %v %v", env, err, called)
	}
	env, _ = r.x.Run(context.Background(), Authorization{op: OpSendThirdParty, vault: testVault}, testReq)
	if env.Outcome != OutcomeRefused || called {
		t.Fatal("an authorization for another operation ran a vault call")
	}
}
