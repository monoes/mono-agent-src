package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/publication"
)

func TestPublicationCLI(t *testing.T) {
	dbPath := newApplicationCLITestDB(t)
	run := func(input string, args ...string) (string, error) {
		cfg := &globalConfig{DBPath: dbPath, JSONOutput: true}
		cmd := newPublicationCmd(cfg)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetIn(strings.NewReader(input))
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
	out, err := run(`{"platform":"x","kind":"post","body":"Published text","idempotency_key":"once"}`, "register", "--stdin-json")
	if err != nil {
		t.Fatal(err)
	}
	var entry publication.Entry
	if err := json.Unmarshal([]byte(out), &entry); err != nil || entry.ID == "" || entry.ProfileID != "default" {
		t.Fatalf("register JSON: %s %v", out, err)
	}
	out, err = run("", "list", "--search", "Published", "--platform", "x", "--limit", "1")
	if err != nil || !strings.Contains(out, entry.ID) {
		t.Fatalf("list JSON: %s %v", out, err)
	}
	out, err = run("", "get", entry.ID)
	if err != nil || !strings.Contains(out, "Published text") {
		t.Fatalf("get: %s %v", out, err)
	}
	out, err = run("", "stats")
	if err != nil || !strings.Contains(out, `"total": 1`) {
		t.Fatalf("stats: %s %v", out, err)
	}
	out, err = run("", "list", "--search", "missing")
	if err != nil || strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty list: %s %v", out, err)
	}
	for _, args := range [][]string{{"list", "--limit", "0"}, {"list", "--since", "invalid"}, {"register"}} {
		if _, err := run("", args...); err == nil {
			t.Errorf("expected invalid input for %v", args)
		}
	}
	_, err = run("", "get", "missing")
	if err == nil || !errors.Is(err, publication.ErrNotFound) && !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing: %v", err)
	}
}
