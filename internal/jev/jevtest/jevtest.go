// Package jevtest is a scripted fake of TypeSafe's /v1/systemone for tests.
// NewServer points TYPESAFE_BASE_URL and jev.DefaultBaseURL at itself (via t.Setenv), records every
// request, and answers every question with a *valid* distribution so the
// client's validation passes unless a handler deliberately breaks it.
//
//	srv := jevtest.NewServer(t, jevtest.Fixed(map[string]string{"route": "billing"}))
//	c, _ := jev.NewClient("test-key", "")
//	… code under test …
//	srv.Requests()  // what was sent
//
// Answer values a Handler returns, per question type:
//   - choice: the option id to pick (missing/unknown → first option in sorted order)
//   - noul:   the probability as a decimal string, e.g. "0.93" (missing → "0.5")
//   - score:  the level index, e.g. "3" (missing → "0")
package jevtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
)

// Handler decides the answer for each question id of one request. Return nil
// to use the defaults for every question.
type Handler func(req jev.Request) map[string]string

// Fixed answers every request with the same map.
func Fixed(answers map[string]string) Handler {
	return func(jev.Request) map[string]string { return answers }
}

// Server is a running fake.
type Server struct {
	*httptest.Server
	mu       sync.Mutex
	requests []jev.Request
	status   int
	handler  Handler
}

// NewServer starts a fake, sets TYPESAFE_BASE_URL (and a dummy
// TYPESAFE_API_KEY when unset) for the test, and closes it on cleanup.
func NewServer(t testing.TB, h Handler) *Server {
	t.Helper()
	s := &Server{handler: h}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	t.Setenv("TYPESAFE_BASE_URL", s.URL)
	// A stored key goes only to the default host (jevconf.BaseURL), so the fake
	// is that host as well.
	prev := jev.DefaultBaseURL
	jev.DefaultBaseURL = s.URL
	t.Cleanup(func() { jev.DefaultBaseURL = prev })
	return s
}

// SetStatus makes every following request fail with code (0 restores answers).
func (s *Server) SetStatus(code int) {
	s.mu.Lock()
	s.status = code
	s.mu.Unlock()
}

// SetHandler swaps the handler for following requests.
func (s *Server) SetHandler(h Handler) {
	s.mu.Lock()
	s.handler = h
	s.mu.Unlock()
}

// Requests returns a copy of every request received so far.
func (s *Server) Requests() []jev.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]jev.Request(nil), s.requests...)
}

// Calls is len(Requests()).
func (s *Server) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

// RequestJSON is the raw JSON of every request — handy for asserting that a
// value never left the machine.
func (s *Server) RequestJSON() string {
	raw, _ := json.Marshal(s.Requests())
	return string(raw)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	var req jev.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	// Decode criteria back into the shapes jev.OptionIDs understands.
	for id, q := range req.Questions {
		q.Criteria = normalizeCriteria(q.Criteria)
		req.Questions[id] = q
	}
	s.mu.Lock()
	s.requests = append(s.requests, req)
	status, h := s.status, s.handler
	s.mu.Unlock()
	if status != 0 {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(status)
		return
	}
	var want map[string]string
	if h != nil {
		want = h(req)
	}
	answers := map[string]jev.Answer{}
	for id, q := range req.Questions {
		a, err := Answer(q, want[id])
		if err != nil {
			http.Error(w, fmt.Sprintf("jevtest: question %q: %v", id, err), http.StatusUnprocessableEntity)
			return
		}
		answers[id] = a
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"model":   "jev-test",
		"answers": answers,
		"usage":   map[string]int{"input_tokens": 100, "output_tokens": 0},
	})
}

// Answer builds a valid answer for q that picks want (see package doc).
func Answer(q jev.Question, want string) (jev.Answer, error) {
	switch q.Type {
	case jev.TypeNoul:
		p := 0.5
		if want != "" {
			v, err := strconv.ParseFloat(want, 64)
			if err != nil || v < 0 || v > 1 {
				return jev.Answer{}, fmt.Errorf("noul answer %q is not a probability", want)
			}
			p = v
		}
		return jev.Answer{Type: jev.TypeNoul, Noul: p}, nil
	case jev.TypeChoice, jev.TypeScore:
	default:
		return jev.Answer{}, fmt.Errorf("unknown type %q", q.Type)
	}
	ids := jev.OptionIDs(q)
	if len(ids) == 0 {
		return jev.Answer{}, fmt.Errorf("no options")
	}
	if q.Type == jev.TypeScore {
		sort.Slice(ids, func(i, j int) bool { a, _ := strconv.Atoi(ids[i]); b, _ := strconv.Atoi(ids[j]); return a < b })
	} else {
		sort.Strings(ids)
	}
	pick := ids[0]
	for _, id := range ids {
		if id == want {
			pick = id
		}
	}
	probs := map[string]float64{}
	for _, id := range ids {
		probs[id] = 0
	}
	// 0.94 on the pick, the rest spread evenly — a realistic, valid answer.
	probs[pick] = 1
	if n := len(ids); n > 1 {
		probs[pick] = 0.94
		for _, id := range ids {
			if id != pick {
				probs[id] = 0.06 / float64(n-1)
			}
		}
	}
	a := jev.Answer{Type: q.Type, Probabilities: probs, Confidence: 0.9}
	if q.Type == jev.TypeChoice {
		a.Choice = pick
	} else {
		a.Score, _ = strconv.ParseFloat(pick, 64)
	}
	return a, nil
}

func normalizeCriteria(c any) any {
	switch v := c.(type) {
	case []any:
		out := make([]string, len(v))
		for i, x := range v {
			out[i] = fmt.Sprint(x)
		}
		return out
	case map[string]any:
		return v
	}
	return c
}

// DefaultHost makes url the TypeSafe host for the test (jev.DefaultBaseURL),
// where a stored key goes; TYPESAFE_BASE_URL is only for TYPESAFE_API_KEY.
func DefaultHost(t testing.TB, url string) {
	t.Helper()
	prev := jev.DefaultBaseURL
	jev.DefaultBaseURL = url
	t.Cleanup(func() { jev.DefaultBaseURL = prev })
}
