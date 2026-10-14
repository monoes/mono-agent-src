package extension

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/monomind"
)

// The knowledge handlers: what the browser is allowed to ask this process
// about the user's own captures (RCL-02, RCL-05).
//
// None of them know anything about the brain. Every answer comes from the
// monomind CLI, which already owns the store, the embedder and the
// provenance — see `monomind doc list|search|cite|related`. This file is the
// adapter between a WebSocket frame and an argv, and its whole job is to
// make the failure modes boring:
//
//   - monomind not installed is Unavailable, not an error. The popup shows
//     nothing rather than a red box, because a user who has not installed
//     the AI engine has not done anything wrong;
//   - every exec is bounded by the request's context, so a wedged CLI
//     cannot outlive the request that started it;
//   - nothing is ever written. These are read-only questions asked from a
//     web page's tab, and the channel they arrive on is reachable by any
//     extension that pairs with this process.

// monomindLookupTimeout bounds the per-exec budget for a lookup. Short: the
// badge is decoration, and a slow answer is worse than no answer because
// the tab has usually moved on.
const monomindLookupTimeout = 8 * time.Second

// monomindWaitDelay is how long a killed monomind gets to release its
// stdout before the pipe is closed out from under whatever is still holding
// it. Long enough that a process exiting normally always gets its output
// read in full; short enough that a leaked grandchild is a blip.
const monomindWaitDelay = 2 * time.Second

// askResultLimit caps how many hits an ask is allowed to return, whatever
// the caller asked for. Each hit costs a `doc cite` exec.
const askResultLimit = 8

// Runner executes one monomind subcommand and returns its stdout. Injected
// so the handlers can be tested without a monomind install (see
// knowledge_test.go).
type Runner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// cliRunner is the real Runner: it locates monomind once and execs it.
type cliRunner struct {
	mu   sync.Mutex
	bin  string
	fail error
}

// Run locates monomind (once, cached) and runs it with args. A missing
// binary is reported as Unavailable so the extension can stay quiet about
// it; a non-zero exit carries stderr, which is where the CLI puts the
// message a human is meant to read.
func (r *cliRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	r.mu.Lock()
	if r.bin == "" && r.fail == nil {
		bin, err := monomind.Find()
		if err != nil {
			// Not cached as permanent: monomind may be installed while
			// the daemon is running, and a user who installs it should
			// not have to restart anything.
			r.mu.Unlock()
			return nil, Unavailable("%s", err.Error())
		}
		r.bin = bin
	}
	bin := r.bin
	r.mu.Unlock()

	cctx, cancel := context.WithTimeout(ctx, monomindLookupTimeout)
	defer cancel()
	cmd := monomind.CommandContext(cctx, bin, args...)
	// Cancelling the context kills monomind; it does not close monomind's
	// stdout, and Output() waits on the PIPE, not on the process. monomind
	// is node, node spawns workers, and any grandchild that inherited the
	// pipe keeps this call blocked for as long as it lives — well past
	// monomindLookupTimeout, holding one of the request channel's eight
	// in-flight slots the whole time. WaitDelay is what turns "wait for
	// whatever still has the pipe" into a bounded wait.
	cmd.WaitDelay = monomindWaitDelay
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if ok := asExitError(err, &ee); ok {
			detail := strings.TrimSpace(string(ee.Stderr))
			if detail == "" {
				detail = err.Error()
			}
			return nil, fmt.Errorf("monomind %s: %s", strings.Join(args, " "), firstLine(detail))
		}
		return nil, fmt.Errorf("monomind %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

func asExitError(err error, target **exec.ExitError) bool {
	ee, ok := err.(*exec.ExitError)
	if ok {
		*target = ee
	}
	return ok
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// SetKnowledgeRunner replaces the monomind runner the knowledge handlers
// use. Tests call it; production does not.
func (s *Server) SetKnowledgeRunner(r Runner) {
	s.knowledgeMu.Lock()
	s.knowledge = r
	s.knowledgeMu.Unlock()
}

func (s *Server) knowledgeRunner() Runner {
	s.knowledgeMu.Lock()
	defer s.knowledgeMu.Unlock()
	if s.knowledge == nil {
		s.knowledge = &cliRunner{}
	}
	return s.knowledge
}

// ---------------------------------------------------------------------------
// The argv gate
// ---------------------------------------------------------------------------

// docTarget is the one gate a caller-supplied URL passes through before it
// can become a word in a `monomind …` argv. It returns the identity URL and
// whether that URL names a page the capture pipeline could ever have saved.
//
// Shared rather than repeated per handler on purpose. doc.related shipped
// calling identityURL alone — which strips a fragment and nothing else — so
// `--config=/home/victim/evil.json`, `--help` and `file:///etc/passwd` all
// reached an exec, while doc.lookup refused exactly the same input two files
// away. Handlers take a URL from a WebSocket frame; they do not each get to
// decide what a URL is.
func docTarget(raw string) (string, bool) {
	target := identityURL(raw)
	if target == "" || !isCapturableURL(target) {
		return target, false
	}
	return target, true
}

// safeArgValue reports whether a string this process did not author may be
// handed to monomind at all. monomind is a Node CLI with its own flag parser
// (packages/@monomind/cli/src/parser.ts), not a shell: nothing here can be
// quoted out of trouble, and a leading dash is the whole attack.
func safeArgValue(v string) bool {
	return v != "" && !strings.HasPrefix(v, "-") && !strings.ContainsAny(v, "\x00\n")
}

// runDoc runs `monomind doc <sub> <flags…> -- <values…>`.
//
// Values are everything this process did not author — a URL from the
// extension, a file path from monomind's own output. They go after `--`,
// where monomind's parser stops reading flags, and they are checked anyway:
// `--` fixes ordering, not content, and it cannot protect a flag VALUE like
// `-q <query>` at all. Callers pass those through safeArgValue themselves.
func runDoc(ctx context.Context, r Runner, sub string, flags []string, values ...string) ([]byte, error) {
	args := make([]string, 0, len(flags)+len(values)+3)
	args = append(args, "doc", sub)
	args = append(args, flags...)
	if len(values) > 0 {
		args = append(args, "--")
		for _, v := range values {
			if !safeArgValue(v) {
				return nil, fmt.Errorf("doc %s: refusing %q as an argument", sub, firstLine(v))
			}
			args = append(args, v)
		}
	}
	return r.Run(ctx, args...)
}

// registerKnowledgeHandlers installs doc.lookup, doc.ask and doc.related.
func registerKnowledgeHandlers(s *Server) {
	s.HandleRequest(MethodDocLookup, s.handleDocLookup)
	s.HandleRequest(MethodDocAsk, s.handleDocAsk)
	s.HandleRequest(MethodDocRelated, s.handleDocRelated)
}

// ---------------------------------------------------------------------------
// doc.lookup (RCL-02)
// ---------------------------------------------------------------------------

// SavedDocument is what the badge and the "already saved" panel render.
type SavedDocument struct {
	Saved bool `json:"saved"`
	// URL is the identity URL the lookup was performed against — the
	// caller's URL with its fragment stripped, which is what the ingest
	// side keys on (captureIdentityUrl in capture-envelope.ts).
	URL        string   `json:"url"`
	Title      string   `json:"title,omitempty"`
	Site       string   `json:"site,omitempty"`
	CapturedAt string   `json:"capturedAt,omitempty"`
	Versions   int      `json:"versions,omitempty"`
	Note       string   `json:"note,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Collection string   `json:"collection,omitempty"`
	Source     string   `json:"source,omitempty"`
	// Path is the indexed file (…/readable.md); Envelope is the capture
	// directory holding it, which is what "open the archived copy" opens.
	Path     string `json:"path,omitempty"`
	Envelope string `json:"envelope,omitempty"`
	// Highlights is how many highlight notes were saved on this page
	// (RCL-04). Zero and absent mean the same thing.
	Highlights int `json:"highlights,omitempty"`
}

// libraryRow is one row of `monomind doc list --json` (LibraryEntry in
// packages/@monomind/cli/src/knowledge/library.ts). Only the fields this
// side uses are declared; the CLI is free to add more.
type libraryRow struct {
	FilePath   string   `json:"filePath"`
	Scope      string   `json:"scope"`
	Title      string   `json:"title"`
	URL        string   `json:"url"`
	Site       string   `json:"site"`
	CapturedAt string   `json:"capturedAt"`
	Source     string   `json:"source"`
	Collection string   `json:"collection"`
	Tags       []string `json:"tags"`
	Version    int      `json:"version"`
	Captured   bool     `json:"captured"`
}

func (s *Server) handleDocLookup(ctx context.Context, req *Request, _ ProgressFunc) (any, error) {
	target, ok := docTarget(req.String("url"))
	if target == "" {
		return nil, &RequestError{Code: CodeUnavailable, Err: fmt.Errorf("doc.lookup needs a url")}
	}
	// Only pages that can actually have been captured. A chrome:// or
	// about: tab is not an absence of a capture, it is not a page, and
	// asking about one is a wasted exec on every new tab.
	if !ok {
		return &SavedDocument{Saved: false, URL: target}, nil
	}
	runner := s.knowledgeRunner()

	scope, err := captureScopeOf(req)
	if err != nil {
		return nil, err
	}

	// The direct answer: one exec, and the only one that carries the NOTE
	// written at save time, which is the whole difference between this and
	// a bookmark. See packages/@monomind/cli/src/knowledge/lookup.ts.
	if doc, err := lookupViaCommand(ctx, runner, target, scope); err == nil {
		return doc, nil
	} else if isUnavailable(err) {
		// monomind is not installed at all — the fallback would fail the
		// same way, and twice as slowly.
		return nil, err
	}

	// The fallback, for a monomind that predates `doc lookup`: read the
	// library and match exactly. `doc list` has no URL filter, so this
	// pulls the whole store — which is why it is the fallback and not the
	// first choice. `--limit` is deliberately NOT passed: it applies after
	// the newest-first sort, so it would hide exactly the old captures
	// this is looking for.
	doc, err := lookupViaLibrary(ctx, runner, target, scope)
	if err != nil {
		return nil, err
	}
	return doc, nil
}

// BrainStatus is how far a profile's captures are from searchable (see
// captureindex.Status, which the host process adapts into this).
type BrainStatus struct {
	Captures  int    `json:"captures"`
	Indexed   int    `json:"indexed"`
	Pending   int    `json:"pending"`
	Failed    int    `json:"failed"`
	LastError string `json:"lastError,omitempty"`
}

// BrainStatusSource answers "how many of this profile's captures are
// searchable". Supplied by the host process, which owns the database.
type BrainStatusSource func(ctx context.Context, profileID string) (*BrainStatus, error)

// SetBrainStatusSource installs the source doc.ask reports Brain from.
func (s *Server) SetBrainStatusSource(src BrainStatusSource) {
	s.knowledgeMu.Lock()
	s.brain = src
	s.knowledgeMu.Unlock()
}

// brainStatus is nil when there is no source or it fails: the status only
// improves the message around the answers, so it never fails the ask.
func (s *Server) brainStatus(ctx context.Context, profileID string) *BrainStatus {
	s.knowledgeMu.Lock()
	src := s.brain
	s.knowledgeMu.Unlock()
	if src == nil {
		return nil
	}
	st, err := src(ctx, profileID)
	if err != nil {
		return nil
	}
	return st
}

// knowledgeState is the knowledge handlers' slice of Server, embedded there
// for the same reason handlerState is.
type knowledgeState struct {
	knowledge   Runner
	brain       BrainStatusSource
	knowledgeMu sync.Mutex
}
