package discovery_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/monoes/mono-agent/internal/discovery"
)

func TestCheckRobotsAllowedPermitsWhenNoDisallow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("User-agent: *\nAllow: /\n"))
	}))
	defer srv.Close()

	if err := discovery.CheckRobotsAllowed(context.Background(), srv.URL, "/api/jobs"); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestCheckRobotsAllowedRefusesAnOversizedRobotsFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("User-agent: *\nAllow: /\n"))
		w.Write(make([]byte, discovery.MaxRobotsBytes+1))
	}))
	defer srv.Close()

	if err := discovery.CheckRobotsAllowed(context.Background(), srv.URL, "/api/jobs"); err == nil {
		t.Fatal("an oversized robots.txt was accepted")
	}
}

func TestDiscoveryClientHasATimeout(t *testing.T) {
	if discovery.Client.Timeout <= 0 {
		t.Fatal("the discovery client waits on the context alone")
	}
}

func TestCheckRobotsAllowedBlocksDisallowedPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("User-agent: *\nDisallow: /api/jobs\n"))
	}))
	defer srv.Close()

	if err := discovery.CheckRobotsAllowed(context.Background(), srv.URL, "/api/jobs"); err == nil {
		t.Fatal("expected an error for a disallowed path, got nil")
	}
}

func TestCheckRobotsAllowedPermitsOn404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if err := discovery.CheckRobotsAllowed(context.Background(), srv.URL, "/api/jobs"); err != nil {
		t.Fatalf("expected no error on 404 (nothing disallowed), got: %v", err)
	}
}

func TestCheckRobotsAllowedIgnoresNamedBotBlocks(t *testing.T) {
	// Only the wildcard "*" block gates this check — a Disallow under a
	// specific named user-agent (e.g. some other crawler) must not affect
	// this source's own fetch.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("User-agent: SomeOtherBot\nDisallow: /api/jobs\n\nUser-agent: *\nAllow: /\n"))
	}))
	defer srv.Close()

	if err := discovery.CheckRobotsAllowed(context.Background(), srv.URL, "/api/jobs"); err != nil {
		t.Fatalf("expected no error (named-bot block should not apply), got: %v", err)
	}
}
