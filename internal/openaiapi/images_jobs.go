package openaiapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// Image jobs: a client that cannot hold a connection open for as long as a runtime
// takes to make an image (a proxy that cuts a request after 100 seconds, say) asks for
// the answer later. POST /v1/images/generations with the header "Prefer: respond-async"
// is answered at once, 202 with a job id; GET /v1/images/jobs/{id} says whether the
// turn is still running and, when it is over, holds what the request would have been
// answered with. A job belongs to the key that made it.
const (
	// maxImageJobs bounds the jobs kept, finished ones and running ones together.
	maxImageJobs = 64
	// maxJobBytes bounds the images that finished jobs hold in memory.
	maxJobBytes = 256 << 20
)

// imageJobTTL is how long a finished job is kept for its client to fetch. A variable
// so a test can shorten it.
var imageJobTTL = 30 * time.Minute

type imageJob struct {
	id, keyID string
	created   int64
	done      bool
	finished  time.Time
	images    [][]byte
	err       *apiError
}

func (j *imageJob) bytes() (n int) {
	for _, b := range j.images {
		n += len(b)
	}
	return n
}

type imageJobs struct {
	now  func() time.Time
	mu   sync.Mutex
	jobs map[string]*imageJob
}

func newImageJobs(now func() time.Time) *imageJobs {
	return &imageJobs{now: now, jobs: map[string]*imageJob{}}
}

// wantsAsync reports whether the request asks to be answered with a job.
func wantsAsync(r *http.Request) bool {
	for _, v := range r.Header.Values("Prefer") {
		for _, tok := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(strings.SplitN(tok, ";", 2)[0]), "respond-async") {
				return true
			}
		}
	}
	return false
}

// add makes a running job for key, after dropping the finished jobs that are too old
// and, while the images held are too many bytes, the oldest finished ones. It reports
// false when every job kept is still running.
func (s *imageJobs) add(keyID string) (*imageJob, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	held := 0
	for id, j := range s.jobs {
		if j.done && now.Sub(j.finished) > imageJobTTL {
			delete(s.jobs, id)
		} else {
			held += j.bytes()
		}
	}
	finished := func() []*imageJob {
		var out []*imageJob
		for _, j := range s.jobs {
			if j.done {
				out = append(out, j)
			}
		}
		slices.SortFunc(out, func(a, b *imageJob) int { return a.finished.Compare(b.finished) })
		return out
	}
	for _, j := range finished() {
		if len(s.jobs) < maxImageJobs && held <= maxJobBytes {
			break
		}
		delete(s.jobs, j.id)
		held -= j.bytes()
	}
	if len(s.jobs) >= maxImageJobs {
		return nil, false
	}
	j := &imageJob{id: newRequestID("imgjob_"), keyID: keyID, created: now.Unix()}
	s.jobs[j.id] = j
	return j, true
}

// finish records the end of a job.
func (s *imageJobs) finish(j *imageJob, images [][]byte, e *apiError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j.images, j.err, j.done, j.finished = images, e, true, s.now()
}

// get returns the job of key with id, nil when there is none (or it is another key's).
func (s *imageJobs) get(keyID, id string) (snapshot imageJob, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[id]
	if j == nil || j.keyID != keyID {
		return imageJob{}, false
	}
	return *j, true
}

func writeJobAccepted(w http.ResponseWriter, j *imageJob) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Location", "/v1/images/jobs/"+j.id)
	w.Header().Set("Retry-After", "5")
	w.WriteHeader(http.StatusAccepted)
	fmt.Fprintf(w, `{"id":%q,"object":"image.job","status":"in_progress","created":%d}`+"\n", j.id, j.created)
}

// runImageJob is the turn of an accepted request, run on its own: the request has been
// answered, and the turn ends at the turn timeout or when the server stops.
func (g *Gateway) runImageJob(j *imageJob, pr Principal, begin time.Time, model, autoBy string, eff Policy, m ModelInfo, slot int, release func(), n int, size, prompt string) {
	ctx, cancel := context.WithCancel(g.shutdownCtx)
	defer cancel()
	status, detail := http.StatusOK, ""
	defer func() { g.logRequest(pr, begin, model, status, "", autoBy, "job "+j.id+detail) }()
	_, imgs, note, e, gone := g.makeImages(ctx, m, eff, pr, slot, release, n, size, prompt)
	switch {
	case gone: // the server stopped
		status = 499
		e = g.cancelledError()
	case e != nil:
		status, detail = e.Status, " "+e.detail
	case note != "":
		detail = " " + note
	}
	g.jobs.finish(j, imgs, e)
}

// handleImageJob is GET /v1/images/jobs/{id}.
func (g *Gateway) handleImageJob() func(http.ResponseWriter, *http.Request, Principal) {
	return func(w http.ResponseWriter, r *http.Request, pr Principal) {
		j, ok := g.jobs.get(pr.KeyID, r.PathValue("id"))
		if !ok {
			writeError(w, &apiError{Status: http.StatusNotFound, Type: "invalid_request_error", Code: "job_not_found", Param: "id",
				Message: "No such image job for this key. A finished job is kept for " + imageJobTTL.String() + "."})
			return
		}
		head := fmt.Sprintf(`"id":%q,"object":"image.job",`, j.id)
		switch {
		case !j.done:
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "5")
			fmt.Fprintf(w, `{%s"status":"in_progress","created":%d}`+"\n", head, j.created)
		case j.err != nil:
			w.Header().Set("Content-Type", "application/json")
			var env struct {
				Error json.RawMessage `json:"error"`
			}
			_ = json.Unmarshal(j.err.body(), &env)
			fmt.Fprintf(w, `{%s"status":"failed","created":%d,"http_status":%d,"error":%s}`+"\n", head, j.created, j.err.Status, env.Error)
		default:
			writeImagesWith(w, head+`"status":"completed",`, j.created, j.images)
		}
	}
}
