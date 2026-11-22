package extension

import (
	"context"
	"strings"
	"testing"
)

// The argv gate.
//
// Everything in this file guards one boundary: a value that arrived over the
// extension WebSocket becoming a word in a `monomind …` argv. monomind is a
// Node CLI with its own flag parser, so a caller-supplied word starting with
// a dash is not inert — `--config=/somewhere/attacker.json` is a real
// capability, and `--help` is a wasted exec at best.

// hostileURLs are what a compromised or malicious page-side caller would put
// in `url`. None of them is a page the capture pipeline could have saved, so
// none of them has any business reaching an exec.
var hostileURLs = []string{
	"--help",
	"--config=/home/victim/evil.json",
	"-v",
	"file:///etc/passwd",
	"chrome://settings",
	"about:blank",
	"javascript:alert(1)",
	"data:text/html,<b>x</b>",
	"/etc/passwd",
}

// doc.lookup already refuses these. doc.related must refuse exactly the same
// set: two handlers taking the same parameter cannot disagree about what it
// is allowed to be.
func TestKnowledgeHandlersNeverExecAHostileURL(t *testing.T) {
	handlers := map[string]func(*Server) func(context.Context, *Request, ProgressFunc) (any, error){
		"doc.lookup":  func(s *Server) func(context.Context, *Request, ProgressFunc) (any, error) { return s.handleDocLookup },
		"doc.related": func(s *Server) func(context.Context, *Request, ProgressFunc) (any, error) { return s.handleDocRelated },
	}
	for method, get := range handlers {
		for _, raw := range hostileURLs {
			runner := newFakeRunner()
			srv := serverWithRunner(t, runner)
			// A profile is named so the profile check cannot be what stops the
			// request: only the URL gate is left between it and the exec.
			req := &Request{Params: map[string]any{"url": raw, "profile": "work"}}
			got, err := get(srv)(context.Background(), req, func(string, string) {})
			// A refusal is an error or an empty answer (no saved page, no related
			// pages); anything else means the URL was treated as a real page.
			switch v := got.(type) {
			case *SavedDocument:
				if err == nil && v.Saved {
					t.Errorf("%s answered a saved page for %q", method, raw)
				}
			case []map[string]any:
				if err == nil && len(v) != 0 {
					t.Errorf("%s answered related pages for %q", method, raw)
				}
			default:
				if err == nil {
					t.Errorf("%s answered %T for %q, want a refusal", method, got, raw)
				}
			}
			if runner.callCount() != 0 {
				t.Errorf("%s exec'd monomind for %q: %v", method, raw, runner.calls)
			}
		}
	}
}

// Every caller-supplied word goes after `--`, where monomind's parser stops
// reading flags (packages/@monomind/cli/src/parser.ts). Defence in depth
// behind the scheme check, not instead of it.
func TestDocRelatedPassesTheURLAfterADoubleDash(t *testing.T) {
	runner := newFakeRunner()
	runner.replies["doc related"] = `[]`
	srv := serverWithRunner(t, runner)

	req := &Request{Params: map[string]any{"url": "https://example.com/post#frag", "profile": "work"}}
	if _, err := srv.handleDocRelated(context.Background(), req, func(string, string) {}); err != nil {
		t.Fatalf("doc.related: %v", err)
	}
	argv := runner.argv("doc related")
	joined := strings.Join(argv, " ")
	if !strings.HasSuffix(joined, "-- https://example.com/post") {
		t.Fatalf("argv = %q, want the url last, behind a -- separator", joined)
	}
	if argv[len(argv)-1] != "https://example.com/post" {
		t.Errorf("target = %q, want the fragment stripped", argv[len(argv)-1])
	}
}

func TestDocLookupPassesTheURLAfterADoubleDash(t *testing.T) {
	runner := newFakeRunner()
	runner.replies["doc lookup"] = `{"saved":false}`
	srv := serverWithRunner(t, runner)

	req := &Request{Params: map[string]any{"url": "https://example.com/post", "profile": "work"}}
	if _, err := srv.handleDocLookup(context.Background(), req, func(string, string) {}); err != nil {
		t.Fatalf("doc.lookup: %v", err)
	}
	joined := strings.Join(runner.argv("doc lookup"), " ")
	if !strings.HasSuffix(joined, "-- https://example.com/post") {
		t.Fatalf("argv = %q, want the url last, behind a -- separator", joined)
	}
}

// doc.ask's question is a flag VALUE (`-q <query>`), which no `--` can
// protect: monomind's parser reads a dash-leading value as the next flag and
// leaves -q set to true. So a question is never allowed to start with a dash.
func TestDocAskNeverLetsAQuestionLookLikeAFlag(t *testing.T) {
	for _, q := range []string{"--config=/home/victim/evil.json", "-v --help", "--help"} {
		runner := newFakeRunner()
		runner.replies["doc search"] = `[]`
		srv := serverWithRunner(t, runner)

		req := &Request{Params: map[string]any{"q": q}}
		if _, err := srv.handleDocAsk(context.Background(), req, func(string, string) {}); err != nil {
			continue // refusing outright is a valid answer
		}
		argv := runner.argv("doc search")
		for i, word := range argv {
			if word == "-q" {
				if i+1 >= len(argv) {
					t.Fatalf("argv = %v, -q has no value", argv)
				}
				if strings.HasPrefix(argv[i+1], "-") {
					t.Errorf("argv = %v, want the question not to read as a flag", argv)
				}
			}
		}
	}
}

// `doc cite` takes a file path from monomind's own search output. It is not
// caller-supplied, but it is not this process's either, and it lands in an
// argv the same way — so it goes through the same gate.
func TestDocCitePassesThePathAfterADoubleDash(t *testing.T) {
	runner := newFakeRunner()
	runner.replies["doc search"] = searchJSON
	runner.replies["doc cite"] = citeJSON
	srv := serverWithRunner(t, runner)

	if _, _ = ask(t, srv, "how much torque"); true {
		argv := runner.argv("doc cite")
		joined := strings.Join(argv, " ")
		if !strings.HasSuffix(joined, "-- /inbox/2026-09-20T09-00-00Z-post/readable.md") {
			t.Fatalf("argv = %q, want the file path last, behind a -- separator", joined)
		}
	}
}

// A hit whose path or anchor arrives looking like a flag is not cited at all
// rather than exec'd hopefully.
func TestDocCiteRefusesAFlagShapedPath(t *testing.T) {
	runner := newFakeRunner()
	runner.replies["doc search"] = `[{"kind":"excerpt","filePath":"--config=/home/victim/evil.json",
	  "scope":"global","text":"x","score":0.5,"chunkIndex":0,
	  "provenance":{"title":"t","url":"https://example.com/x"}}]`
	srv := serverWithRunner(t, runner)

	result, _ := ask(t, srv, "anything")
	if runner.argv("doc cite") != nil {
		t.Fatalf("cite argv = %v, want no exec at all", runner.argv("doc cite"))
	}
	if len(result.Answers) != 1 {
		t.Errorf("answers = %d, want the hit kept with its own chunk text", len(result.Answers))
	}
}
