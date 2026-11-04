package extension

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
