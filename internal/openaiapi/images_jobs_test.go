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
