package openaiapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/monomind"
)

// One key that keeps submitting jobs must not push another key's finished results out
// before their TTL.
func TestImageJobStoreOneKeyCannotEvictAnotherKeysResults(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newImageJobs(func() time.Time { return now })
	a, _ := s.add("a")
	s.finish(a, [][]byte{{1}}, nil, nil)
	for i := 0; i < 3*maxImageJobs; i++ {
		now = now.Add(time.Second)
		j, ok := s.add("b")
		if !ok {
			t.Fatalf("refused job %d", i)
		}
		s.finish(j, [][]byte{{2}}, nil, nil)
	}
	if _, ok := s.get("a", a.id); !ok {
		t.Error("key a's finished job was evicted by key b's submissions")
	}
	n := 0
	for _, j := range s.jobs {
		if j.keyID == "b" {
			n++
		}
	}
	if n > maxJobsPerKey {
		t.Errorf("key b holds %d jobs, want at most %d", n, maxJobsPerKey)
	}
}

func startBlockedJob(t *testing.T, h *harness, secret string) string {
	t.Helper()
	rec := postImagesAsync(h, secret, `{"model":"codex","prompt":"x"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var acc struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &acc)
	return acc.ID
}

// blockingHarness runs a turn that waits for its context to end.
func blockingHarness(t *testing.T) (*harness, chan struct{}, chan struct{}) {
	started, ended := make(chan struct{}, 1), make(chan struct{}, 1)
	h := newHarness(t, func(ctx context.Context, o monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error) {
		started <- struct{}{}
		<-ctx.Done()
		ended <- struct{}{}
		return nil, ctx.Err()
	}, func(_ *Deps, c *Config) { c.MaxConcurrent = 1 })
	return h, started, ended
}

func waitEnded(t *testing.T, ended <-chan struct{}) {
	t.Helper()
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the job's turn was never cancelled")
	}
}

func TestImageJobCanBeCancelledByItsKey(t *testing.T) {
	h, started, ended := blockingHarness(t)
	secret := h.key(t, "default", "app", false)
	other := h.key(t, "default", "other", false)
	id := startBlockedJob(t, h, secret)
	waitStarted(t, started)

	if rec := h.serve(anyPolicy, http.MethodDelete, jobsURL+id, other, ""); rec.Code != http.StatusNotFound {
		t.Errorf("another key's DELETE got %d", rec.Code)
	}
	rec := h.serve(anyPolicy, http.MethodDelete, jobsURL+id, secret, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"cancelled":true`) {
		t.Fatalf("DELETE got %d: %s", rec.Code, rec.Body)
	}
	waitEnded(t, ended)
	if rec := h.serve(anyPolicy, http.MethodGet, jobsURL+id, secret, ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET after DELETE got %d", rec.Code)
	}
}

func TestImageJobEndsWhenItsKeyIsRevoked(t *testing.T) {
	old := keyCheckEvery
	keyCheckEvery = 10 * time.Millisecond
	t.Cleanup(func() { keyCheckEvery = old })
	h, started, ended := blockingHarness(t)
	secret := h.key(t, "default", "app", false)
	startBlockedJob(t, h, secret)
	waitStarted(t, started)
	if _, err := h.keys.Revoke(context.Background(), "default", "app"); err != nil {
		t.Fatal(err)
	}
	waitEnded(t, ended)
}

// A dedicated listener on a loopback address is not served to a page that reaches it
// under another name (DNS rebinding): the HTTP API's Host guard applies.
func TestServeOnLoopbackRefusesAForeignHost(t *testing.T) {
	h := newHarness(t, okTurn("x"))
	addr, stop := startServe(t, h, anyPolicy, nil)
	defer func() { _ = stop() }()
	port := addr[strings.LastIndex(addr, ":")+1:]
	for host, want := range map[string]int{addr: http.StatusOK, "evil.example:" + port: http.StatusMisdirectedRequest} {
		req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/health", nil)
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("Host %q got %d, want %d", host, resp.StatusCode, want)
		}
	}
}

func TestAcceptedJobLogLineIsMarked(t *testing.T) {
	h := newHarness(t, (&execLog{}).imageExec("image-1.png", onePNG("image-1.png")))
	secret := h.key(t, "default", "app", false)
	id := startBlockedJob(t, h, secret)
	waitJob(t, h, secret, id)
	found := false
	for _, l := range h.logged() {
		if strings.Contains(l, "status=202") && strings.Contains(l, "job accepted "+id) {
			found = true
		}
	}
	if !found {
		t.Errorf("no marked 202 line in %q", h.logged())
	}
}
