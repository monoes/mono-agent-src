package openaiapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/monomind"
)

const jobsURL = "/v1/images/jobs/"

func postImagesAsync(h *harness, secret, body string) *httpRecorder {
	r := newJSONRequest(http.MethodPost, imagesURL, secret, body)
	r.Header.Set("Prefer", "respond-async")
	return h.do(anyPolicy, r)
}

func newJSONRequest(method, path, secret, body string) *http.Request {
	r, _ := http.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+secret)
	return r
}

// waitJob polls the job until it is no longer running.
func waitJob(t *testing.T, h *harness, secret, id string) *httpRecorder {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		rec := h.serve(anyPolicy, http.MethodGet, jobsURL+id, secret, "")
		var j struct{ Status string }
		_ = json.Unmarshal(rec.Body.Bytes(), &j)
		if j.Status != "in_progress" {
			return rec
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the job never finished")
	return nil
}

func TestImageJobCompletes(t *testing.T) {
	log := &execLog{}
	h := newHarness(t, log.imageExec("image-1.png", onePNG("image-1.png")))
	secret := h.key(t, "default", "app", false)

	rec := postImagesAsync(h, secret, `{"model":"codex","prompt":"a red circle"}`)
	if rec.Code != http.StatusAccepted || !strings.HasPrefix(rec.Header().Get("Location"), jobsURL+"imgjob_") {
		t.Fatalf("status %d, Location %q, body %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	var acc struct{ ID, Object, Status string }
	if err := json.Unmarshal(rec.Body.Bytes(), &acc); err != nil || acc.Status != "in_progress" || acc.Object != "image.job" {
		t.Fatalf("accepted body %s (%v)", rec.Body, err)
	}

	done := waitJob(t, h, secret, acc.ID)
	if done.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", done.Code, done.Body)
	}
	var out struct {
		ID, Status string
		Data       []map[string]string
		Usage      *usage
	}
	if err := json.Unmarshal(done.Body.Bytes(), &out); err != nil || out.Status != "completed" || out.ID != acc.ID || len(out.Data) != 1 || out.Data[0]["b64_json"] == "" || out.Usage == nil || out.Usage.PromptTokens != 40000 {
		t.Fatalf("finished body %s (%v)", done.Body, err)
	}
	// still there for a second read
	if again := h.serve(anyPolicy, http.MethodGet, jobsURL+acc.ID, secret, ""); again.Code != http.StatusOK || !strings.Contains(again.Body.String(), `"completed"`) {
		t.Errorf("second read: %d %s", again.Code, again.Body)
	}
}

func TestImageJobFailureIsReported(t *testing.T) {
	log := &execLog{}
	h := newHarness(t, log.imageExec("sorry", nil)) // the runtime saves nothing
	secret := h.key(t, "default", "app", false)

	rec := postImagesAsync(h, secret, `{"model":"codex","prompt":"a red circle"}`)
	var acc struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &acc)
	done := waitJob(t, h, secret, acc.ID)
	var out struct {
		Status     string
		HTTPStatus int `json:"http_status"`
		Error      struct{ Code string }
	}
	if err := json.Unmarshal(done.Body.Bytes(), &out); err != nil || out.Status != "failed" || out.HTTPStatus != http.StatusBadGateway || out.Error.Code != "image_generation_failed" {
		t.Fatalf("body %s (%v)", done.Body, err)
	}
}

func TestImageJobBelongsToItsKey(t *testing.T) {
	log := &execLog{}
	h := newHarness(t, log.imageExec("image-1.png", onePNG("image-1.png")))
	a := h.key(t, "default", "a", false)
	b := h.key(t, "default", "b", false)

	var acc struct{ ID string }
	_ = json.Unmarshal(postImagesAsync(h, a, `{"model":"codex","prompt":"x"}`).Body.Bytes(), &acc)
	if rec := h.serve(anyPolicy, http.MethodGet, jobsURL+acc.ID, b, ""); rec.Code != http.StatusNotFound {
		t.Errorf("another key got %d: %s", rec.Code, rec.Body)
	}
	if rec := h.serve(anyPolicy, http.MethodGet, jobsURL+"imgjob_nope", a, ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown id got %d", rec.Code)
	}
	if rec := h.serve(anyPolicy, http.MethodGet, jobsURL+acc.ID, "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no key got %d", rec.Code)
	}
	waitJob(t, h, a, acc.ID)
}

func TestImageJobRefusalsAreSynchronous(t *testing.T) {
	h := newHarness(t, (&execLog{}).imageExec("x", nil))
	secret := h.key(t, "default", "app", false)
	if rec := postImagesAsync(h, secret, `{"model":"codex"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("a request without a prompt got %d: %s", rec.Code, rec.Body)
	}
}

func TestImageJobKeepsTheSlotUntilTheTurnEnds(t *testing.T) {
	started, release := make(chan struct{}, 1), make(chan struct{})
	h := newHarness(t, func(ctx context.Context, o monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error) {
		started <- struct{}{}
		<-release
		return imageTurn("x", onePNG("a.png"))(ctx, o, onEvent)
	}, func(_ *Deps, c *Config) { c.MaxConcurrent = 1 })
	secret := h.key(t, "default", "app", false)

	rec := postImagesAsync(h, secret, `{"model":"codex","prompt":"x"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	waitStarted(t, started)
	var acc struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &acc)
	if got := h.serve(anyPolicy, http.MethodGet, jobsURL+acc.ID, secret, ""); !strings.Contains(got.Body.String(), `"in_progress"`) {
		t.Errorf("running job: %s", got.Body)
	}
	// with the turn still running, the slot is taken (the harness has one)
	if other := postImages(h, anyPolicy, secret, `{"model":"codex","prompt":"y"}`); other.Code != http.StatusTooManyRequests {
		t.Errorf("a second request while the job runs got %d", other.Code)
	}
	close(release)
	waitJob(t, h, secret, acc.ID)
}

func TestImageJobStoreEvictsOldFinishedJobs(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newImageJobs(func() time.Time { return now })
	j, _ := s.add("k")
	s.finish(j, [][]byte{{1}}, nil, nil)
	now = now.Add(imageJobTTL + time.Second)
	s.add("k")
	if _, ok := s.get("k", j.id); ok {
		t.Error("a finished job older than the TTL is kept")
	}
	for i := 0; i < maxImageJobs; i++ {
		if _, ok := s.add("k"); !ok && i < maxImageJobs-1 {
			t.Fatalf("refused job %d", i)
		}
	}
	if _, ok := s.add("k"); ok {
		t.Error("more running jobs than the cap")
	}
}

// A panic inside the job's goroutine has no net/http recover above it and would end the
// gateway. The job fails with a 500, and the slot it held is free for the next one.
func TestImageJobThatPanicsFailsAndFreesItsSlot(t *testing.T) {
	var calls int32
	h := newHarness(t, func(ctx context.Context, opts monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			panic("boom")
		}
		return imageTurn("image-1.png", onePNG("image-1.png"))(ctx, opts, onEvent)
	})
	secret := h.key(t, "default", "app", false)

	var acc struct{ ID string }
	_ = json.Unmarshal(postImagesAsync(h, secret, `{"model":"codex","prompt":"a red circle"}`).Body.Bytes(), &acc)
	done := waitJob(t, h, secret, acc.ID)
	var out struct {
		Status     string
		HTTPStatus int `json:"http_status"`
	}
	if err := json.Unmarshal(done.Body.Bytes(), &out); err != nil || out.Status != "failed" || out.HTTPStatus != http.StatusInternalServerError {
		t.Fatalf("a job that panicked answers %s (%v)", done.Body, err)
	}
	if strings.Contains(done.Body.String(), "boom") {
		t.Errorf("the panic value reaches the client: %s", done.Body)
	}

	rec := postImagesAsync(h, secret, `{"model":"codex","prompt":"again"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("the slot of the crashed job was not given back: %d %s", rec.Code, rec.Body)
	}
	var next struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &next)
	if again := waitJob(t, h, secret, next.ID); !strings.Contains(again.Body.String(), `"completed"`) {
		t.Errorf("the job after the crash did not complete: %s", again.Body)
	}
}

func TestImageJobStoreFailsAJobThatNeverEndsSoItFreesItsPlace(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newImageJobs(func() time.Time { return now })
	s.stuckAfter = time.Minute
	stuck, _ := s.add("k")
	for i := 1; i < maxImageJobs; i++ {
		if _, ok := s.add("k"); !ok {
			t.Fatalf("refused job %d", i)
		}
	}
	if _, ok := s.add("k"); ok {
		t.Fatal("more running jobs than the cap")
	}
	now = now.Add(2 * time.Minute)
	if _, ok := s.add("k"); !ok {
		t.Fatal("jobs that never end hold every place for ever")
	}
	got, ok := s.get("k", stuck.id)
	if !ok || !got.done || got.err == nil {
		t.Fatalf("the stuck job is %+v (found %v): it should be failed", got, ok)
	}
}

func TestImageJobFinishingAfterBeingDroppedAsStuckChangesNothing(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newImageJobs(func() time.Time { return now })
	s.stuckAfter = time.Minute
	j, _ := s.add("k")
	now = now.Add(2 * time.Minute)
	s.add("k")
	s.finish(j, [][]byte{{1, 2, 3}}, nil, nil)
	if j.err == nil || len(j.images) != 0 {
		t.Errorf("a late answer replaced the one the client was told: err %v, images %d", j.err, len(j.images))
	}
}

// The byte bound holds when images arrive, not only when the next job is added.
func TestImageJobFinishDropsOlderFinishedJobsWhileTooManyBytesAreHeld(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newImageJobs(func() time.Time { return now })
	big := make([]byte, maxJobBytes/2+1)
	// All three are running before any finishes, so only finish can enforce the bound.
	var running []*imageJob
	for i := 0; i < 3; i++ {
		j, ok := s.add("k")
		if !ok {
			t.Fatal("refused")
		}
		running = append(running, j)
	}
	first := running[0]
	for _, j := range running {
		now = now.Add(time.Second)
		s.finish(j, [][]byte{big}, nil, nil)
	}
	if _, ok := s.get("k", first.id); ok {
		t.Error("the oldest finished job is kept while the images held are over the bound")
	}
	held := 0
	for _, j := range s.jobs {
		held += j.bytes()
	}
	if held > maxJobBytes+len(big) {
		t.Errorf("%d bytes held after finish, bound %d", held, maxJobBytes)
	}
}
