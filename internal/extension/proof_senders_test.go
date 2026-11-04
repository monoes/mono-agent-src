package extension

import (
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Every function that sends the stored token must reach a listener that has
// proven it holds it. This pins it at the source: a function that attaches the
// token header (tokenHeader as the header name of a Set call) must call the
// prover (prove / proveBridge) in the same function; and nothing outside this
// package may name the header, since it could not use the prover.
func TestSourcePin_EveryTokenSenderGoesThroughTheProver(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	senders := 0
	for _, pkg := range pkgs {
		for file, f := range pkg.Files {
			for _, d := range f.Decls {
				fn, ok := d.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				sets, proves := false, false
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					switch fun := call.Fun.(type) {
					case *ast.SelectorExpr:
						if fun.Sel.Name == "Set" && len(call.Args) > 0 {
							if id, ok := call.Args[0].(*ast.Ident); ok && id.Name == "tokenHeader" {
								sets = true
							}
						}
						if fun.Sel.Name == "prove" || fun.Sel.Name == "proveBridge" {
							proves = true
						}
					case *ast.Ident:
						if fun.Name == "proveBridge" {
							proves = true
						}
					}
					return true
				})
				if sets {
					senders++
					if !proves {
						t.Errorf("%s: %s attaches the pairing token header without going through prove()/proveBridge(): "+
							"a listener that has not proven it holds the token would receive it", filepath.Base(file), fn.Name.Name)
					}
				}
			}
		}
	}
	// every request goes through newBridgeClient (it refuses redirects): no other http.Client literal
	for _, pkg := range pkgs {
		for file, f := range pkg.Files {
			for _, d := range f.Decls {
				fn, ok := d.(*ast.FuncDecl)
				if !ok || fn.Body == nil || fn.Name.Name == "newBridgeClient" {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					if cl, ok := n.(*ast.CompositeLit); ok {
						if se, ok := cl.Type.(*ast.SelectorExpr); ok && se.Sel.Name == "Client" {
							if id, ok := se.X.(*ast.Ident); ok && id.Name == "http" {
								t.Errorf("%s: %s builds its own http.Client: use newBridgeClient (it refuses redirects, which would carry the token and body elsewhere)", filepath.Base(file), fn.Name.Name)
							}
						}
					}
					return true
				})
			}
		}
	}
	if senders < 4 {
		t.Fatalf("found %d token senders, expected at least 4 (the pin is not looking at the right code)", senders)
	}
	// nothing outside the package spells the header
	root := filepath.Join("..", "..")
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git" || d.Name() == "wails-app") {
			return filepath.SkipDir
		}
		if d.IsDir() && filepath.Base(p) == "extension" && filepath.Base(filepath.Dir(p)) == "internal" {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, _ := os.ReadFile(p)
		if strings.Contains(string(b), "X-Monoagent-Extension-Token") {
			t.Errorf("%s names the extension token header: only internal/extension may send it (through the prover)", p)
		}
		return nil
	})
}

// A listener that never proves: every sender must stop at the status request
// and never send the token.
func TestEverySenderRefusesAListenerThatNeverProves(t *testing.T) {
	withTempHome(t)
	token, err := generateToken()
	if err != nil {
		t.Fatal(err)
	}
	senders := map[string]func(base string){
		"SendCommandTo": func(b string) { _, _ = NewRemoteSender(b).SendCommand(&Command{Type: CmdCreateTab}, time.Second) },
		"Browsers":      func(b string) { _, _ = NewRemoteSender(b).Browsers() },
		"ResolveTarget": func(b string) { _, _ = NewRemoteSender(b).ResolveTarget(Target{}) },
		"CapturePage":   func(b string) { _, _ = NewRemoteSender(b).CapturePage(CaptureRequest{}) },
		"CheckPairing":  func(b string) { _, _ = CheckPairing(b) },
		"RequestPairingURL": func(b string) {
			_, _ = RequestPairingURL(b)
		},
	}
	for name, send := range senders {
		t.Run(name, func(t *testing.T) {
			f := newFakeBridge(t, nil)
			send(f.URL)
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.reqs) == 0 {
				t.Fatal("the sender never even probed the listener")
			}
			first := f.reqs[0]
			if first.method != http.MethodGet || first.path != healthPath || !strings.HasPrefix(first.query, nonceParam+"=") || first.token != "" {
				t.Errorf("the first request must be the nonce status request with no token header, got %+v", first)
			}
			for _, r := range f.reqs {
				if r.token != "" || strings.Contains(r.query, token) {
					t.Errorf("the token reached a listener that never proved it: %+v", r)
				}
			}
			if len(f.reqs) != 1 {
				t.Errorf("requests after the failed proof: %+v", f.reqs)
			}
		})
	}
}

// Candidates: a proving bridge on one and a proof-less squatter on the other.
// The token goes to the proving one only, whichever is listed first, even when
// the squatter would be acceptable as an old bridge.
func TestPickBridge_PrefersAProvingCandidateOverAProoflessSquatter(t *testing.T) {
	withTempHome(t)
	token, _ := generateToken()
	for _, squatterFirst := range []bool{true, false} {
		real := newFakeBridge(t, func(n, port string) string { return proofFor(token, n, healthPath, "127.0.0.1", port) })
		squatter := newFakeBridge(t, nil)
		addrs := []string{real.URL, squatter.URL}
		if squatterFirst {
			addrs = []string{squatter.URL, real.URL}
		}
		got, ok := pickBridge(addrs, token)
		if !ok || got != real.URL {
			t.Fatalf("squatterFirst=%v: picked %q ok=%v, want the proving bridge %s", squatterFirst, got, ok, real.URL)
		}
		if err := relayOnce(got); err != nil {
			t.Fatal(err)
		}
		if squatter.tokenSeen() {
			t.Fatal("the squatter received the token")
		}
		if !real.tokenSeen() {
			t.Fatal("the proving bridge did not get the relay")
		}
	}
	// only a squatter: nothing is picked, and using it sends no token
	sq := newFakeBridge(t, nil)
	if got, ok := pickBridge([]string{sq.URL}, token); ok {
		t.Errorf("picked a proof-less listener %q", got)
	}
	if err := relayOnce(sq.URL); !errors.Is(err, ErrBridgeNoProof) || sq.tokenSeen() {
		t.Errorf("squatter only: err=%v tokenSeen=%v", err, sq.tokenSeen())
	}
}

// The proof is bound to the host the client dialled.
func TestProof_BoundToTheDialledHost(t *testing.T) {
	withTempHome(t)
	token, _ := generateToken()
	f := newFakeBridge(t, func(n, port string) string { return proofFor(token, n, healthPath, "::1", port) })
	if err := relayOnce(f.URL); !errors.Is(err, ErrBridgeBadProof) {
		t.Fatalf("a proof for [::1] accepted for a 127.0.0.1 dial: %v", err)
	}
}

// One classified answer per probe result: older bridge, token mismatch, and
// neither (not a bridge / nothing there) which leaves the caller's own wording.
func TestDiagnoseBridges_ClassifiesTheCandidates(t *testing.T) {
	withTempHome(t)
	token, _ := generateToken()
	old := newFakeBridge(t, nil)
	wrong := newFakeBridge(t, func(n, port string) string { return proofFor("other", n, healthPath, "127.0.0.1", port) })
	notBridge := httptest.NewServer(http.NotFoundHandler())
	defer notBridge.Close()
	good := newFakeBridge(t, func(n, port string) string { return proofFor(token, n, healthPath, "127.0.0.1", port) })

	if a, err := DiagnoseBridges([]string{notBridge.URL, old.URL}); a != old.URL || !errors.Is(err, ErrBridgeNoProof) ||
		!strings.Contains(err.Error(), "older than this CLI") || strings.Contains(err.Error(), "Chrome") {
		t.Errorf("old bridge: %q %v", a, err)
	}
	a, err := DiagnoseBridges([]string{wrong.URL, notBridge.URL})
	if a != wrong.URL || !errors.Is(err, ErrBridgeBadProof) {
		t.Fatalf("mismatch: %q %v", a, err)
	}
	for _, hint := range []string{"extension status", "re-pair", "pairing token"} {
		if !strings.Contains(err.Error(), hint) {
			t.Errorf("the mismatch text lacks %q (the pairing hint): %v", hint, err)
		}
	}
	if a, err := DiagnoseBridges([]string{notBridge.URL, "http://127.0.0.1:1"}); err != nil || a != "" {
		t.Errorf("not a bridge / nothing there: %q %v", a, err)
	}
	if _, err := DiagnoseBridges([]string{good.URL}); err != nil {
		t.Errorf("a proving bridge: %v", err)
	}
	// older wins over mismatch, whatever the order
	if _, err := DiagnoseBridges([]string{wrong.URL, old.URL}); !errors.Is(err, ErrBridgeNoProof) {
		t.Errorf("old + mismatch: %v", err)
	}
	// the same classification through CheckPairing: a wrong proof is the pairing mismatch
	if st, err := CheckPairing(wrong.URL); st != PairingMismatch || err != nil {
		t.Errorf("CheckPairing on a different token: %v %v", st, err)
	}
	if st, err := CheckPairing(old.URL); st != PairingUnknown || !errors.Is(err, ErrBridgeNoProof) {
		t.Errorf("CheckPairing on an older bridge: %v %v", st, err)
	}
}

// A status answer is never followed to another address, and a huge one is cut
// off: the probe must not be steered by (or choked on) a listener's reply.
func TestVerifyBridge_DoesNotFollowRedirectsAndCapsTheBody(t *testing.T) {
	withTempHome(t)
	token, _ := generateToken()
	var mu sync.Mutex
	hit := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hit++
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(Status{Service: ServiceName})
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.RedirectHandler(target.URL+healthPath, http.StatusFound))
	defer redirector.Close()
	err := verifyBridge(newBridgeClient(0), redirector.URL, token)
	if err == nil || !strings.Contains(err.Error(), "HTTP 302") {
		t.Fatalf("redirecting listener: %v, want it refused as not a bridge (HTTP 302)", err)
	}
	mu.Lock()
	if hit != 0 {
		t.Errorf("the redirect was followed (%d hits)", hit)
	}
	mu.Unlock()

	var sent int64
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := []byte(strings.Repeat(" ", 32<<10))
		for i := 0; i < 1600; i++ { // 50 MB if nobody stops reading
			n, err := w.Write(chunk)
			atomic.AddInt64(&sent, int64(n))
			if err != nil {
				return
			}
		}
	}))
	defer big.Close()
	if err := verifyBridge(newBridgeClient(0), big.URL, token); err == nil {
		t.Fatal("an endless answer was accepted")
	}
	if got := atomic.LoadInt64(&sent); got > 8<<20 {
		t.Errorf("the probe let %d bytes through: the body is not capped", got)
	}
}

// A bridge that proved itself and then answers a token-carrying request with a
// redirect: the second listener must receive nothing (no token, no body), and the
// caller gets an error. One case per token sender.
func TestEveryTokenSenderRefusesToFollowARedirect(t *testing.T) {
	withTempHome(t)
	token, _ := generateToken()
	senders := map[string]func(base string) error{
		"SendCommandTo": func(b string) error {
			_, err := NewRemoteSender(b).SendCommand(&Command{Type: CmdCreateTab, Params: map[string]interface{}{"url": "secret-body"}}, time.Second)
			return err
		},
		"Browsers":          func(b string) error { _, err := NewRemoteSender(b).Browsers(); return err },
		"ResolveTarget":     func(b string) error { _, err := NewRemoteSender(b).ResolveTarget(Target{}); return err },
		"CapturePage":       func(b string) error { _, err := NewRemoteSender(b).CapturePage(CaptureRequest{}); return err },
		"CheckPairing":      func(b string) error { _, err := CheckPairing(b); return err },
		"RequestPairingURL": func(b string) error { _, err := RequestPairingURL(b); return err },
	}
	for name, send := range senders {
		t.Run(name, func(t *testing.T) {
			second := newFakeBridge(t, nil) // records everything it is sent
			var mu sync.Mutex
			code := 307
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == healthPath {
					st := Status{Service: ServiceName}
					if n := r.URL.Query().Get(nonceParam); n != "" {
						st.Proof = proofFor(token, n, healthPath, "127.0.0.1", strings.TrimPrefix(r.Host, "127.0.0.1:"))
					}
					_ = json.NewEncoder(w).Encode(st)
					return
				}
				mu.Lock()
				c := code
				mu.Unlock()
				http.Redirect(w, r, second.URL+r.URL.Path, c)
			}))
			defer first.Close()
			_ = send(first.URL)
			second.mu.Lock()
			defer second.mu.Unlock()
			if len(second.reqs) != 0 {
				t.Fatalf("the redirect was followed: the second listener got %+v", second.reqs)
			}
		})
	}
	// the error is clear for the relay
	second := newFakeBridge(t, nil)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == healthPath {
			st := Status{Service: ServiceName}
			if n := r.URL.Query().Get(nonceParam); n != "" {
				st.Proof = proofFor(token, n, healthPath, "127.0.0.1", strings.TrimPrefix(r.Host, "127.0.0.1:"))
			}
			_ = json.NewEncoder(w).Encode(st)
			return
		}
		http.Redirect(w, r, second.URL, http.StatusFound)
	}))
	defer first.Close()
	_, err := NewRemoteSender(first.URL).SendCommand(&Command{Type: CmdCreateTab}, time.Second)
	if err == nil || !strings.Contains(err.Error(), "302") {
		t.Errorf("relay redirected: err = %v, want a clear 302 error", err)
	}
}
