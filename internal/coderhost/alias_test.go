package coderhost

import (
	"context"
	"strings"
	"testing"
)

func TestAliasFromTitle(t *testing.T) {
	for in, want := range map[string]string{
		"meta challenge":          "meta-challenge",
		"  Meta   Challenge  ":    "Meta-Challenge",
		"fix: the login (v2)!":    "fix-the-login-v2",
		"api_v1.2 - draft":        "api_v1.2-draft",
		"--lead-dashes":           "lead-dashes",
		"._hidden":                "hidden",
		"trailing dash -":         "trailing-dash",
		"émoji 🚀 only":            "moji-only",
		"🚀🚀":                      "",
		"":                        "",
		strings.Repeat("ab ", 40): strings.TrimRight(strings.Repeat("ab-", 40)[:64], "-"),
	} {
		got := AliasFromTitle(in)
		if got != want {
			t.Errorf("AliasFromTitle(%q) = %q, want %q", in, got, want)
		}
		if got != "" && !meshAlias.MatchString(got) {
			t.Errorf("AliasFromTitle(%q) = %q, which the settings would refuse", in, got)
		}
	}
}

func TestAMeshSessionWithoutAnAliasTakesTheChatsName(t *testing.T) {
	h, _, st := newHost(t)
	rm := &recordingMesh{}
	h.Mesh = rm
	addConversation(t, st, "c1", "/work/c1")
	st.EnsureSession("p", "c1")
	if _, err := st.db.Exec(`UPDATE ai_chat_conversations SET title = 'meta challenge' WHERE id = 'c1'`); err != nil {
		t.Fatal(err)
	}
	h.meshSync(context.Background())
	if s := rm.last(); len(s) != 1 || s[0].Alias != "meta-challenge" {
		t.Fatalf("alias = %+v, want meta-challenge", s)
	}
	// Renaming the chat changes what the mesh is asked for, so the session re-registers.
	st.db.Exec(`UPDATE ai_chat_conversations SET title = 'second name' WHERE id = 'c1'`)
	h.meshSync(context.Background())
	if s := rm.last(); s[0].Alias != "second-name" {
		t.Fatalf("alias = %q after the rename, want second-name", s[0].Alias)
	}
	// An alias the person set wins over the name.
	cur, _ := st.GetSettings("p", "c1")
	cur.MeshAlias = "mine"
	if err := st.SaveSettings("p", "c1", cur); err != nil {
		t.Fatal(err)
	}
	h.meshSync(context.Background())
	if s := rm.last(); s[0].Alias != "mine" {
		t.Fatalf("alias = %q, want the one that was set", s[0].Alias)
	}
	// No name at all: the mesh makes one from the project and time.
	cur.MeshAlias = ""
	st.SaveSettings("p", "c1", cur)
	st.db.Exec(`UPDATE ai_chat_conversations SET title = '' WHERE id = 'c1'`)
	h.meshSync(context.Background())
	if s := rm.last(); s[0].Alias != "" {
		t.Fatalf("alias = %q, want empty (made by the mesh)", s[0].Alias)
	}
	// A TitleOf (the app's derived title) is used when set.
	h.TitleOf = func(_, id string) string { return "Derived " + id }
	h.meshSync(context.Background())
	if s := rm.last(); s[0].Alias != "Derived-c1" {
		t.Fatalf("alias = %q, want Derived-c1", s[0].Alias)
	}
}
