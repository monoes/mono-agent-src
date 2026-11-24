//go:build !windows

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// The People, Profile and post pages read and write through `people …` and
// `list ls`, not the database.
func TestPeopleBindingsShellOut(t *testing.T) {
	log := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, `echo "$*" >> '`+log+`'
case "$*" in
  *" people list "*) echo '[{"id":"p1","platform_username":"sam","platform":"LINKEDIN","full_name":"Sam","image_url":"i","profile_url":"u","follower_count":"5","following_count":2,"is_verified":true,"created_at":"2026-09-01 10:00:00"}]' ;;
  *" people count"*) echo '{"count":42}' ;;
  *" people get "*) echo '{"id":"p1","platform_username":"sam","platform":"LINKEDIN","content_count":3,"profile_url":"u","introduction":"hi","created_at":"2026-09-01T10:00:00Z","updated_at":"2026-09-02T10:00:00Z","links":[],"profile_details":{"likes_count":5600,"links":[{"url":"https://example.test/"}]}}' ;;
  *" people interactions "*) echo '[{"execution_id":"e1","node_name":"Like","status":"COMPLETED"}]' ;;
  *" posts list "*) echo '[{"id":"po1","url":"x","like_count":5,"we_liked":true}]' ;;
  *" posts get "*) echo '{"id":"po1","shortcode":"abc","caption":"c"}' ;;
  *" posts comments "*) echo '[{"id":"c1","author":"al","likes_count":1}]' ;;
  *" status get "*) echo 'null' ;;
  *" status set "*) echo '{"id":"s1","person_id":"p1","text":"closed","created_at":"2026-09-26T10:00:00Z"}' ;;
  *" status history "*) echo '[{"id":"s1","person_id":"p1","text":"closed","created_at":"2026-09-26T10:00:00Z"}]' ;;
  *" tag list"*) echo '[{"id":"t1","name":"hot","color":"#fff"}]' ;;
  *" tag map "*) echo '{"p1":[{"id":"t1","name":"hot","color":"#fff"}]}' ;;
  *" tag add "*) echo '{"id":"t1","name":"hot","color":"#fff"}' ;;
  *" list ls"*) echo '[{"id":"l1","name":"Leads","list_type":"people","item_count":3,"created_at":"2026-09-01T10:00:00Z"}]' ;;
  *) echo '{}' ;;
esac
`))
	a := newTestApp(t)
	a.ctx = context.Background()
	a.selectForTest("work")

	people, err := a.GetPeople("ALL", "", 50, 100)
	if err != nil || len(people) != 1 || people[0].Username != "sam" || people[0].ProfileURL != "u" || !people[0].IsVerified || people[0].CreatedAt == "" {
		t.Fatalf("GetPeople = %+v", people)
	}
	_, _ = a.GetPeople("instagram", "-sam", 0, 10)
	if n, err := a.GetPeopleCount("LINKEDIN", "sam"); err != nil || n != 42 {
		t.Fatalf("GetPeopleCount = %d", n)
	}
	if n, err := a.GetPeopleCount("", ""); err != nil || n != 42 {
		t.Fatalf("GetPeopleCount (no filter) = %d", n)
	}
	if p, err := a.GetPersonDetail("p1"); err != nil || p == nil || p.Username != "sam" || p.ContentCount != 3 || p.ProfileURL != "u" || p.UpdatedAt == "" ||
		p.ProfileDetails["likes_count"] != float64(5600) || p.ProfileDetails["links"] == nil {
		t.Fatalf("GetPersonDetail = %+v", p)
	}
	if i, err := a.GetPersonInteractions("p1"); err != nil || len(i) != 1 || i[0].NodeName != "Like" {
		t.Fatalf("GetPersonInteractions = %+v", i)
	}
	if p := a.GetPersonPosts("p1"); len(p) != 1 || !p[0].WeLiked || p[0].LikeCount != 5 {
		t.Fatalf("GetPersonPosts = %+v", p)
	}
	if p := a.GetPostDetail("po1"); p == nil || p.Shortcode != "abc" {
		t.Fatalf("GetPostDetail = %+v", p)
	}
	if c := a.GetPostComments("po1"); len(c) != 1 || c[0].LikesCount != 1 {
		t.Fatalf("GetPostComments = %+v", c)
	}
	if s := a.GetLatestPersonStatus("p1"); s != nil {
		t.Fatalf("GetLatestPersonStatus with none = %+v", s)
	}
	if s, err := a.AddPersonStatus("p1", "-closed"); err != nil || s.ID != "s1" {
		t.Fatalf("AddPersonStatus = %+v, %v", s, err)
	}
	if h := a.GetPersonStatusHistory("p1", 0); len(h) != 1 || h[0].Text != "closed" {
		t.Fatalf("GetPersonStatusHistory = %+v", h)
	}
	a.GetPersonStatusHistory("p1", 5)
	if tags := a.GetAllTags(); len(tags) != 1 || tags[0].Name != "hot" {
		t.Fatalf("GetAllTags = %+v", tags)
	}
	if tags := a.GetPersonTags("p1"); len(tags) != 1 {
		t.Fatalf("GetPersonTags = %+v", tags)
	}
	if m := a.GetPeopleTagsMap([]string{"p1", "p2"}); len(m["p1"]) != 1 || len(m["p2"]) != 0 {
		t.Fatalf("GetPeopleTagsMap = %+v", m)
	}
	if m := a.GetPeopleTagsMap(nil); m != nil {
		t.Fatalf("GetPeopleTagsMap(nil) = %+v", m)
	}
	if tag := a.AddPersonTag("p1", "hot", "#fff"); tag == nil || tag.ID != "t1" {
		t.Fatalf("AddPersonTag = %+v", tag)
	}
	if !a.UpdateTagColor("t1", "#000") {
		t.Fatal("UpdateTagColor failed")
	}
	a.RemovePersonTag("p1", "t1")
	if l := a.GetSocialLists(); len(l) != 1 || l[0].ItemCount != 3 || l[0].ListType != "people" || l[0].CreatedAt == "" {
		t.Fatalf("GetSocialLists = %+v", l)
	}

	want := []string{
		"--profile work --json people list --limit 50 --offset 100",
		"--profile work --json people list --platform=instagram --search=-sam --limit 0",
		"--profile work --json people count --platform=LINKEDIN --search=sam",
		"--profile work --json people count",
		"--profile work --json people get p1",
		"--profile work --json people interactions p1",
		"--profile work --json people posts list p1",
		"--profile work --json people posts get po1",
		"--profile work --json people posts comments po1",
		"--profile work --json people status get p1",
		"--profile work --json people status set -- p1 -closed",
		"--profile work --json people status history p1",
		"--profile work --json people status history p1 --limit 5",
		"--profile work --json people tag list",
		"--profile work --json people tag list --person=p1",
		"--profile work --json people tag map -- p1 p2",
		"--profile work --json people tag add --color=#fff -- p1 hot",
		"--profile work --json people tag color -- t1 #000",
		"--profile work --json people tag remove -- p1 t1",
		"--profile work --json list ls",
	}
	if got := loggedArgs(t, log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("argv:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The compose and draft bindings go through `people messages …`.
func TestPersonMessageWriteBindingsShellOut(t *testing.T) {
	log := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, `echo "$*" >> '`+log+`'
case "$*" in
  *" messages compose "*) echo '{"id":"m1","person_id":"p1","source":"outlook","direction":"outbound","status":"draft","created_at":"2026-09-26T10:00:00Z"}' ;;
  *" messages drafts"*) echo '[{"id":"m1","person_id":"p1","source":"outlook","direction":"outbound","status":"draft","created_at":"2026-09-26T10:00:00Z","person_platform_username":"a@x.com"}]' ;;
  *" messages send-draft "*) echo '{"id":"m1","person_id":"p1","source":"outlook","direction":"outbound","status":"sent","external_id":"g2","created_at":"2026-09-26T10:00:00Z"}' ;;
  *" messages reject-draft bad"*) echo 'message bad is not a draft' >&2; exit 1 ;;
  *) echo '{}' ;;
esac
`))
	a := newTestApp(t)
	a.ctx = context.Background()
	a.selectForTest("work")

	m, err := a.ComposePersonMessage("p1", "outlook", "Hi", "<p>body</p>", true)
	if err != nil || m.ID != "m1" || m.Status != "draft" {
		t.Fatalf("ComposePersonMessage = %+v, %v", m, err)
	}
	if _, err := a.ComposePersonMessage("p1", "c1", "", "b", false); err != nil {
		t.Fatal(err)
	}
	if d, err := a.GetDraftPersonMessages(); err != nil || len(d) != 1 || d[0].PersonPlatformUsername != "a@x.com" {
		t.Fatalf("GetDraftPersonMessages = %+v, %v", d, err)
	}
	if m, err := a.SendDraftPersonMessage("m1"); err != nil || m.Status != "sent" || m.ExternalID != "g2" {
		t.Fatalf("SendDraftPersonMessage = %+v, %v", m, err)
	}
	if err := a.RejectDraftPersonMessage("m1"); err != nil {
		t.Fatal(err)
	}
	if err := a.RejectDraftPersonMessage("bad"); err == nil || !strings.Contains(err.Error(), "not a draft") {
		t.Fatalf("RejectDraftPersonMessage(bad) = %v", err)
	}

	want := []string{
		"--profile work --json people messages add --source=manual --subject=Hi --body=hello there -- p1",
		"--profile work --json people messages add --source=outlook --external-id=x1 --direction=outbound --sender=me -- p1",
		"--profile work --json people messages compose --connection=outlook --subject=Hi --body=<p>body</p> --draft -- p1",
		"--profile work --json people messages compose --connection=c1 --subject= --body=b -- p1",
		"--profile work --json people messages drafts",
		"--profile work --json people messages send-draft m1",
		"--profile work --json people messages reject-draft m1",
		"--profile work --json people messages reject-draft bad",
	}
	if got := loggedArgs(t, log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("argv:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
