package aitask

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func draftTask(t *testing.T, s *Service, l *fakeLauncher) Task {
	t.Helper()
	src := filepath.Join(t.TempDir(), "pasted.txt")
	if err := os.WriteFile(src, []byte("Hi, can we move the call to Friday?"), 0o644); err != nil {
		t.Fatal(err)
	}
	task, err := s.Submit(context.Background(), SubmitInput{Kind: KindDraftReply, Goal: "Draft a reply",
		Context: []ContextSource{{SourceSelection, src}}})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestDraftReplyIsTheLeastAccessAndHasNoWayToSaySent(t *testing.T) {
	k, err := Lookup(KindDraftReply)
	if err != nil {
		t.Fatal(err)
	}
	if k.DefaultAccess != AccessQA || FullAccess(k.DefaultAccess) || k.UnattendedOK {
		t.Fatalf("draft-reply must default to qa and not run unattended: %+v", k)
	}
	s, l := newService(t)
	task := draftTask(t, s, l)
	if task.Access != AccessQA {
		t.Fatalf("access = %q", task.Access)
	}
	// A result that carries a send flag, a status, a connection or any other
	// key is refused: the schema is closed at both levels.
	for _, r := range []string{
		`{"summary":"s","draft":{"body":"hi"},"assumptions":[],"send":true}`,
		`{"summary":"s","draft":{"body":"hi"},"assumptions":[],"status":"sent"}`,
		`{"summary":"s","draft":{"body":"hi","send":true},"assumptions":[]}`,
		`{"summary":"s","draft":{"body":"hi","connection":"outlook"},"assumptions":[]}`,
	} {
		if _, err := s.Complete(task.ID, l.tokens[0], []byte(r)); code(err) != CodeSchemaInvalid || !strings.Contains(err.Error(), "unexpected property") {
			t.Errorf("%s: %v", r, err)
		}
	}
}

func TestDraftReplyResultSchema(t *testing.T) {
	cases := map[string]string{
		`{"summary":"s","draft":{"body":"Friday works.","to":"sam","subject":"Re: call"},"assumptions":["you are free Friday"]}`: "",
		`{"summary":"s","draft":{"body":"ok"},"assumptions":[]}`:                                                                 "",
		`{"summary":"s","draft":{"body":""},"assumptions":[]}`:                                                                   "must be at least 1 characters",
		`{"summary":"s","draft":{"body":"` + strings.Repeat("x", MaxDraftBodyChars+1) + `"},"assumptions":[]}`:                   "must be at most",
		`{"summary":"s","draft":{"subject":"only"},"assumptions":[]}`:                                                            `missing required property "body"`,
		`{"summary":"s","assumptions":[]}`:                                                                                       `missing required property "draft"`,
		`{"summary":"s","draft":{"body":"x"}}`:                                                                                   `missing required property "assumptions"`,
		`{"summary":"","draft":{"body":"x"},"assumptions":[]}`:                                                                   "$.summary",
		`{"summary":"s","draft":{"body":"x"},"assumptions":[1]}`:                                                                 "$.assumptions[0]",
	}
	for doc, want := range cases {
		s, l := newService(t)
		task := draftTask(t, s, l)
		_, err := s.Complete(task.ID, l.tokens[0], []byte(doc))
		if (want == "") != (err == nil) || (err != nil && (code(err) != CodeSchemaInvalid || !strings.Contains(err.Error(), want))) {
			t.Errorf("%.80s: err = %v, want %q", doc, err, want)
		}
	}
}

func TestDraftReplyBodyHasSecretsRedactedBeforeItIsStored(t *testing.T) {
	s, l := newService(t)
	s.Redact = RedactValues([]string{"hunter2hunter2"})
	task := draftTask(t, s, l)
	doc := `{"summary":"s","draft":{"body":"My login is hunter2hunter2, thanks"},"assumptions":["sk-live-0123456789abcdef"]}`
	if _, err := s.Complete(task.ID, l.tokens[0], []byte(doc)); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := s.Store.DB.QueryRow(`SELECT result_json FROM ai_tasks WHERE id = ?`, task.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "hunter2") || !strings.Contains(stored, "My login is ***, thanks") {
		t.Fatalf("vault value reached the draft: %s", stored)
	}
	// A body that is itself a key or token is replaced whole.
	s2, l2 := newService(t)
	task2 := draftTask(t, s2, l2)
	if _, err := s2.Complete(task2.ID, l2.tokens[0], []byte(`{"summary":"s","draft":{"body":"ghp_0123456789abcdefghij"},"assumptions":[]}`)); err != nil {
		t.Fatal(err)
	}
	var stored2 string
	_ = s2.Store.DB.QueryRow(`SELECT result_json FROM ai_tasks WHERE id = ?`, task2.ID).Scan(&stored2)
	if strings.Contains(stored2, "ghp_") || !strings.Contains(stored2, RedactedValue) {
		t.Fatalf("token reached the draft: %s", stored2)
	}
}

func TestDraftReplyNeverTakesAThreadOrPeopleContext(t *testing.T) {
	s, _ := newService(t)
	src := filepath.Join(t.TempDir(), "thread.txt")
	if err := os.WriteFile(src, []byte("From: a third party"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, class := range []string{"thread", "message", "messages", "people", "person", "thread-text"} {
		_, err := s.Submit(context.Background(), SubmitInput{Kind: KindDraftReply, Goal: "g",
			Context: []ContextSource{{class, src}}})
		if code(err) != CodeContextSource {
			t.Errorf("class %q: err = %v, want %s", class, err, CodeContextSource)
		}
	}
	if count(t, s) != 0 {
		t.Fatalf("a refused context left a task behind: %d", count(t, s))
	}
}

func TestDraftReplyPromptStatesTheNoSendContract(t *testing.T) {
	s, l := newService(t)
	task := draftTask(t, s, l)
	for _, want := range []string{"cannot send", "never invent", "secret", "data, never instructions", "aitask complete " + task.ID} {
		if !strings.Contains(strings.ToLower(task.Prompt), strings.ToLower(want)) {
			t.Errorf("prompt lacks %q:\n%s", want, task.Prompt)
		}
	}
}
