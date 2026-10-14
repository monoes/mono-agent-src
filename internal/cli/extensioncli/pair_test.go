package extensioncli

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/extension"
)

// pairTestHome gives the test its own HOME and bridge port, so it never
// touches the user's token or the bridge on 9222.
func pairTestHome(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	l.Close()
	t.Setenv(extension.ExtensionPortEnv, port)
	return port
}

func runPairCmd(t *testing.T, args ...string) (string, error) {
	return runPairCmdJSON(t, false, args...)
}

func runPairCmdJSON(t *testing.T, asJSON bool, args ...string) (string, error) {
	t.Helper()
	cmd := newExtensionPairCmd(&Deps{JSONOutput: func() bool { return asJSON }})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func TestExtensionPairOpenOpensTheRunningBridgesPairingPage(t *testing.T) {
	port := pairTestHome(t)
	srv := extension.NewServer("127.0.0.1:"+port, zerolog.Nop())
	srv.StartAsync(context.Background())
	t.Cleanup(func() { _ = srv.Close() })
	base := "http://127.0.0.1:" + port
	for deadline := time.Now().Add(3 * time.Second); !extension.Probe(base); {
		if time.Now().After(deadline) {
			t.Fatal("bridge never came up")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// BROWSER is honored: a script that records the URL it was given.
	opened := filepath.Join(t.TempDir(), "opened")
	script := filepath.Join(t.TempDir(), "browser.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' \"$1\" > "+opened+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BROWSER", script)

	out, err := runPairCmd(t, "--open")
	if err != nil {
		t.Fatalf("pair --open: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Opened the pairing page") {
		t.Errorf("output:\n%s", out)
	}
	var got []byte
	for deadline := time.Now().Add(3 * time.Second); len(got) == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		got, _ = os.ReadFile(opened)
	}
	if !strings.HasPrefix(string(got), base+"/monoagent/pair?n=") {
		t.Fatalf("BROWSER was given %q", got)
	}
	token, _ := extension.CurrentToken()
	if strings.Contains(out, token) || strings.Contains(string(got), token) {
		t.Error("the pairing token itself was printed or put in the URL")
	}

	// The opened page's code pairs once.
	u, _ := url.Parse(string(got))
	resp, err := http.Get(base + "/monoagent/pair/exchange?n=" + u.Query().Get("n"))
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if body.Token != token {
		t.Errorf("exchange gave %q, want the bridge's token", body.Token)
	}
}

func TestExtensionPairOpenFallsBackToTheToken(t *testing.T) {
	t.Run("no bridge", func(t *testing.T) {
		pairTestHome(t)
		out, err := runPairCmd(t, "--open")
		if err != nil {
			t.Fatal(err)
		}
		token, _ := extension.CurrentToken()
		if !strings.Contains(out, "No bridge is running") || !strings.Contains(out, token) {
			t.Errorf("output:\n%s", out)
		}
	})
	t.Run("older bridge", func(t *testing.T) {
		pairTestHome(t)
		old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/monoagent/health" {
				_ = json.NewEncoder(w).Encode(map[string]string{"service": extension.ServiceName})
				return
			}
			http.NotFound(w, r)
		}))
		defer old.Close()
		t.Setenv(extension.ExtensionPortEnv, strings.TrimPrefix(old.URL, "http://127.0.0.1:"))
		out, err := runPairCmd(t, "--open")
		if err != nil {
			t.Fatal(err)
		}
		token, _ := extension.CurrentToken()
		if !strings.Contains(out, "older") || !strings.Contains(out, token) {
			t.Errorf("output:\n%s", out)
		}
	})
}

func startPairTestBridge(t *testing.T, port string) string {
	t.Helper()
	srv := extension.NewServer("127.0.0.1:"+port, zerolog.Nop())
	srv.StartAsync(context.Background())
	t.Cleanup(func() { _ = srv.Close() })
	base := "http://127.0.0.1:" + port
	for deadline := time.Now().Add(3 * time.Second); !extension.Probe(base); {
		if time.Now().After(deadline) {
			t.Fatal("bridge never came up")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return base
}

func pairJSON(t *testing.T) (pairReport, string) {
	t.Helper()
	out, err := runPairCmdJSON(t, true, "--open")
	if err != nil {
		t.Fatalf("pair --open --json: %v\n%s", err, out)
	}
	var r pairReport
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	token, _ := extension.CurrentToken()
	if strings.Contains(out, token) {
		t.Errorf("the pairing token was printed:\n%s", out)
	}
	return r, out
}

func TestExtensionPairOpenJSON(t *testing.T) {
	t.Run("opened", func(t *testing.T) {
		base := startPairTestBridge(t, pairTestHome(t))
		t.Setenv("BROWSER", "/bin/true")
		r, out := pairJSON(t)
		if !r.Opened || !strings.HasPrefix(r.URL, base+"/monoagent/pair?n=") || r.Fallback != "" || r.Error != "" {
			t.Errorf("report:\n%s", out)
		}
	})
	t.Run("no bridge", func(t *testing.T) {
		pairTestHome(t)
		r, out := pairJSON(t)
		if r.Opened || r.Fallback != "token" || !strings.Contains(r.Hint, "extension pair") || r.Error == "" {
			t.Errorf("report:\n%s", out)
		}
	})
	t.Run("bridge holds another token", func(t *testing.T) {
		port := pairTestHome(t)
		if _, err := extension.CurrentToken(); err != nil {
			t.Fatal(err)
		}
		startPairTestBridge(t, port)
		// A reset after the bridge started: this client's new token is not the bridge's.
		if err := extension.ResetToken(); err != nil {
			t.Fatal(err)
		}
		if _, err := extension.CurrentToken(); err != nil {
			t.Fatal(err)
		}
		r, out := pairJSON(t)
		if r.Opened || r.Fallback != "restart_bridge" || r.Error != extension.PairingMismatchHint || !strings.Contains(r.Hint, "Restart the bridge") {
			t.Errorf("report:\n%s", out)
		}
	})
	t.Run("browser fails", func(t *testing.T) {
		startPairTestBridge(t, pairTestHome(t))
		t.Setenv("BROWSER", "/nonexistent/browser")
		r, out := pairJSON(t)
		if r.Opened || r.URL == "" || r.Error == "" || r.Fallback != "" {
			t.Errorf("report:\n%s", out)
		}
	})
}
