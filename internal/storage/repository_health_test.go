package storage_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
)

// addPerson stores a person through the normal upsert path and returns its id.
func addPerson(t *testing.T, db *storage.Database, username, platform, profile string) string {
	t.Helper()
	p := &storage.Person{PlatformUsername: username, Platform: platform, ProfileID: profile, FullName: strings.ToUpper(username)}
	if err := db.UpsertPerson(p); err != nil {
		t.Fatalf("UpsertPerson(%s): %v", username, err)
	}
	got, err := db.GetPersonByUsername(username, platform, profile)
	if err != nil || got == nil {
		t.Fatalf("GetPersonByUsername(%s): %v, %v", username, got, err)
	}
	return got.ID
}

// --- people ----------------------------------------------------------------

func TestHealthGetListDeletePeople(t *testing.T) {
	db := testdb.Open(t)
	alice := addPerson(t, db, "alice", "x", "")
	addPerson(t, db, "bob", "x", "")
	addPerson(t, db, "alina", "instagram", "")

	p, err := db.GetPerson(alice)
	if err != nil || p == nil {
		t.Fatalf("GetPerson: %v %v", p, err)
	}
	if p.PlatformUsername != "alice" || p.FullName != "ALICE" || p.Platform != "x" {
		t.Errorf("GetPerson = %+v", p)
	}
	if missing, err := db.GetPerson("nope"); err != nil || missing != nil {
		t.Errorf("GetPerson(missing) = %v, %v", missing, err)
	}
	if missing, err := db.GetPersonByUsername("alice", "instagram", ""); err != nil || missing != nil {
		t.Errorf("GetPersonByUsername on wrong platform = %v, %v", missing, err)
	}

	names := func(ps []*storage.Person) map[string]bool {
		m := map[string]bool{}
		for _, p := range ps {
			m[p.PlatformUsername] = true
		}
		return m
	}
	all, err := db.ListPeople("", "", 0, -1)
	if err != nil || len(all) != 3 {
		t.Fatalf("ListPeople all = %d, %v", len(all), err)
	}
	onX, _ := db.ListPeople("x", "", 10, 0)
	if n := names(onX); len(n) != 2 || !n["alice"] || !n["bob"] {
		t.Errorf("ListPeople platform=x = %v", n)
	}
	// Search matches username or full name, case-insensitively for ASCII.
	ali, _ := db.ListPeople("", "ali", 10, 0)
	if n := names(ali); len(n) != 2 || !n["alice"] || !n["alina"] {
		t.Errorf("ListPeople search=ali = %v", n)
	}
	byName, _ := db.ListPeople("x", "BOB", 10, 0)
	if n := names(byName); len(n) != 1 || !n["bob"] {
		t.Errorf("ListPeople search=BOB on x = %v", n)
	}
	page1, _ := db.ListPeople("", "", 2, 0)
	page2, _ := db.ListPeople("", "", 2, 2)
	if len(page1) != 2 || len(page2) != 1 {
		t.Errorf("pagination = %d + %d, want 2 + 1", len(page1), len(page2))
	}

	if err := db.DeletePerson(alice); err != nil {
		t.Fatalf("DeletePerson: %v", err)
	}
	if err := db.DeletePerson(alice); err == nil {
		t.Error("second DeletePerson should report not found")
	}
}

func TestHealthPeopleProfileIsolation(t *testing.T) {
	db := testdb.Open(t)
	a := addPerson(t, db, "same", "x", "default")
	b := addPerson(t, db, "same", "x", "work")
	if a == b {
		t.Fatal("same username on two profiles must be two rows")
	}
	if p, _ := db.GetPersonByUsername("same", "x", "work"); p == nil || p.ID != b || p.ProfileID != "work" {
		t.Errorf("work lookup = %+v", p)
	}
	// An empty profile means "default".
	if p, _ := db.GetPersonByUsername("same", "x", ""); p == nil || p.ID != a {
		t.Errorf("default lookup = %+v", p)
	}
}

func TestHealthDeletePersonCascadesHistory(t *testing.T) {
	db := testdb.Open(t)
	id := addPerson(t, db, "carol", "email", "")
	msg := &storage.PersonMessage{PersonID: id, Source: "outlook", Body: "hi"}
	if err := db.UpsertPersonMessage(msg, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddPersonStatusUpdate(id, "", "met at conf"); err != nil {
		t.Fatal(err)
	}
	if err := db.DeletePerson(id); err != nil {
		t.Fatal(err)
	}
	if m, _ := db.GetPersonMessage(msg.ID); m != nil {
		t.Error("message survived its person's delete")
	}
	if u, _ := db.GetLatestPersonStatusUpdate(id, ""); u != nil {
		t.Error("status update survived its person's delete")
	}
}

func TestHealthBatchUpsertPeople(t *testing.T) {
	db := testdb.Open(t)
	if err := db.BatchUpsertPeople(nil); err != nil {
		t.Fatalf("empty batch: %v", err)
	}
	batch := []*storage.Person{
		{PlatformUsername: "p1", Platform: "x", FullName: "One", FollowingCount: 3},
		{PlatformUsername: "p2", Platform: "x", ProfileID: "work", IsVerified: true},
	}
	if err := db.BatchUpsertPeople(batch); err != nil {
		t.Fatalf("BatchUpsertPeople: %v", err)
	}
	if batch[0].ID == "" || batch[0].ProfileID != "default" {
		t.Errorf("batch did not fill id/profile: %+v", batch[0])
	}
	p1, _ := db.GetPersonByUsername("p1", "x", "")
	if p1 == nil || p1.FullName != "One" || p1.FollowingCount != 3 {
		t.Errorf("p1 = %+v", p1)
	}
	p2, _ := db.GetPersonByUsername("p2", "x", "work")
	if p2 == nil || !p2.IsVerified {
		t.Errorf("p2 = %+v", p2)
	}

	// Re-upserting the same key updates in place instead of adding a row.
	if err := db.BatchUpsertPeople([]*storage.Person{{PlatformUsername: "p1", Platform: "x", FullName: "Uno"}}); err != nil {
		t.Fatal(err)
	}
	all, _ := db.ListPeople("x", "", 0, 0)
	if len(all) != 2 {
		t.Errorf("rows after re-upsert = %d, want 2", len(all))
	}
	p1, _ = db.GetPersonByUsername("p1", "x", "")
	if p1.FullName != "Uno" {
		t.Errorf("re-upsert did not update full_name: %q", p1.FullName)
	}
}

// UpsertPerson keeps enrichment a partial write leaves out (see
// TestUpsertPersonPreservesEnrichmentOnPartialUpsert); the batch variant
// overwrites every column with whatever the batch carries.
func TestHealthBatchUpsertPeoplePreservesEnrichment(t *testing.T) {
	db := testdb.Open(t)
	full := &storage.Person{PlatformUsername: "e", Platform: "x", FullName: "Enriched", JobTitle: "CTO", FollowingCount: 9}
	if err := db.UpsertPerson(full); err != nil {
		t.Fatal(err)
	}
	if err := db.BatchUpsertPeople([]*storage.Person{{PlatformUsername: "e", Platform: "x"}}); err != nil {
		t.Fatal(err)
	}
	p, _ := db.GetPersonByUsername("e", "x", "")
	if p.FullName != "Enriched" || p.JobTitle != "CTO" || p.FollowingCount != 9 {
		t.Errorf("partial batch upsert wiped enrichment: %+v", p)
	}
}

// --- person messages ---------------------------------------------------------

func TestHealthPersonMessagesUpsertIdempotentByExternalID(t *testing.T) {
	db := testdb.Open(t)
	id := addPerson(t, db, "dave", "email", "")

	m := &storage.PersonMessage{PersonID: id, Source: "outlook", ExternalID: "ext-1", Subject: "v1"}
	if err := db.UpsertPersonMessage(m, ""); err != nil {
		t.Fatal(err)
	}
	if m.Direction != "inbound" || m.Status != "sent" {
		t.Errorf("defaults = %q/%q, want inbound/sent", m.Direction, m.Status)
	}
	// Same (person, source, external_id): an update, not a second row.
	if err := db.UpsertPersonMessage(&storage.PersonMessage{PersonID: id, Source: "outlook", ExternalID: "ext-1", Subject: "v2"}, ""); err != nil {
		t.Fatal(err)
	}
	// Same external id from another source is a different message.
	if err := db.UpsertPersonMessage(&storage.PersonMessage{PersonID: id, Source: "gmail", ExternalID: "ext-1", Subject: "other"}, ""); err != nil {
		t.Fatal(err)
	}
	// No external id: defaults to the message id, so notes never collide.
	n1 := &storage.PersonMessage{PersonID: id, Source: "note", Body: "a"}
	n2 := &storage.PersonMessage{PersonID: id, Source: "note", Body: "b"}
	if err := db.UpsertPersonMessage(n1, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertPersonMessage(n2, ""); err != nil {
		t.Fatal(err)
	}
	if n1.ExternalID != n1.ID || n2.ExternalID != n2.ID {
		t.Errorf("external ids default to own id: %q/%q", n1.ExternalID, n1.ID)
	}

	all, err := db.ListPersonMessages(id, "", "", 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("messages = %d, want 4", len(all))
	}
	outlook, _ := db.ListPersonMessages(id, "outlook", "", 10, 0)
	if len(outlook) != 1 || outlook[0].Subject != "v2" || outlook[0].ID != m.ID {
		t.Errorf("outlook messages after re-upsert = %+v", outlook)
	}
}

func TestHealthPersonMessagesProfileIsolation(t *testing.T) {
	db := testdb.Open(t)
	home := addPerson(t, db, "erin", "x", "default")
	work := addPerson(t, db, "erin", "x", "work")

	// A message must belong to a person of the profile it is written under.
	if err := db.UpsertPersonMessage(&storage.PersonMessage{PersonID: work, Source: "x"}, "default"); err == nil {
		t.Error("writing a message for another profile's person should fail")
	}
	if err := db.UpsertPersonMessage(&storage.PersonMessage{PersonID: "ghost", Source: "x"}, ""); err == nil {
		t.Error("writing a message for an unknown person should fail")
	}
	if err := db.UpsertPersonMessage(&storage.PersonMessage{PersonID: work, Source: "x", Body: "w"}, "work"); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertPersonMessage(&storage.PersonMessage{PersonID: home, Source: "x", Body: "h"}, ""); err != nil {
		t.Fatal(err)
	}

	if got, _ := db.ListPersonMessages(work, "", "default", 10, 0); len(got) != 0 {
		t.Errorf("default profile sees work person's messages: %d", len(got))
	}
	if got, _ := db.ListPersonMessages(work, "", "work", 10, 0); len(got) != 1 || got[0].Body != "w" {
		t.Errorf("work messages = %+v", got)
	}
	homeAll, _ := db.ListAllPersonMessages("", "", 10, 0)
	if len(homeAll) != 1 || homeAll[0].Body != "h" || homeAll[0].PersonPlatformUsername != "erin" || homeAll[0].PersonFullName != "ERIN" {
		t.Errorf("ListAllPersonMessages(default) = %+v", homeAll)
	}
	workAll, _ := db.ListAllPersonMessages("work", "", 10, 0)
	if len(workAll) != 1 || workAll[0].Body != "w" {
		t.Errorf("ListAllPersonMessages(work) = %+v", workAll)
	}
}

func TestHealthListAllPersonMessagesOrderSourceAndPaging(t *testing.T) {
	db := testdb.Open(t)
	id := addPerson(t, db, "fay", "email", "")
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	for i, src := range []string{"outlook", "gmail", "outlook", "outlook"} {
		m := &storage.PersonMessage{PersonID: id, Source: src, Subject: fmt.Sprintf("m%d", i), SentAt: base.Add(time.Duration(i) * time.Hour)}
		if err := db.UpsertPersonMessage(m, ""); err != nil {
			t.Fatal(err)
		}
	}
	subjects := func(ms []*storage.PersonMessageWithPerson) string {
		var s []string
		for _, m := range ms {
			s = append(s, m.Subject)
		}
		return strings.Join(s, ",")
	}
	all, err := db.ListAllPersonMessages("", "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if subjects(all) != "m3,m2,m1,m0" {
		t.Errorf("newest first = %s", subjects(all))
	}
	if !all[0].SentAt.Equal(base.Add(3 * time.Hour)) {
		t.Errorf("sent_at round trip = %v", all[0].SentAt)
	}
	outlook, _ := db.ListAllPersonMessages("", "outlook", 2, 1)
	if subjects(outlook) != "m2,m0" {
		t.Errorf("outlook page 2 = %s, want m2,m0", subjects(outlook))
	}
	perPerson, _ := db.ListPersonMessages(id, "", "", 1, 1)
	if len(perPerson) != 1 || perPerson[0].Subject != "m2" {
		t.Errorf("ListPersonMessages limit 1 offset 1 = %+v", perPerson)
	}
}

func TestHealthPersonMessageStatusAndDrafts(t *testing.T) {
	db := testdb.Open(t)
	gus := addPerson(t, db, "gus", "email", "")
	other := addPerson(t, db, "gus", "email", "work")

	draft := &storage.PersonMessage{PersonID: gus, Source: "outlook", Direction: "outbound", Status: "draft", Subject: "hello"}
	if err := db.UpsertPersonMessage(draft, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertPersonMessage(&storage.PersonMessage{PersonID: other, Source: "outlook", Status: "draft"}, "work"); err != nil {
		t.Fatal(err)
	}
	drafts, err := db.ListPersonMessagesByStatus("", "draft")
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 1 || drafts[0].ID != draft.ID || drafts[0].PersonPlatform != "email" {
		t.Fatalf("default drafts = %+v", drafts)
	}

	if err := db.UpdatePersonMessageStatus(draft.ID, "sent"); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdatePersonMessageExternalID(draft.ID, "graph-123"); err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetPersonMessage(draft.ID)
	if got.Status != "sent" || got.ExternalID != "graph-123" || got.Direction != "outbound" {
		t.Errorf("after status/external id update: %+v", got)
	}
	if drafts, _ := db.ListPersonMessagesByStatus("", "draft"); len(drafts) != 0 {
		t.Errorf("draft list after send = %d", len(drafts))
	}

	if err := db.UpdatePersonMessageStatus("missing", "sent"); err == nil {
		t.Error("status update of a missing message should fail")
	}
	if err := db.UpdatePersonMessageExternalID("missing", "x"); err == nil {
		t.Error("external id update of a missing message should fail")
	}
	if err := db.DeletePersonMessage(draft.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.DeletePersonMessage(draft.ID); err == nil {
		t.Error("deleting a missing message should fail")
	}
	if m, err := db.GetPersonMessage(draft.ID); err != nil || m != nil {
		t.Errorf("GetPersonMessage after delete = %v, %v", m, err)
	}
}

func TestHealthUnreadFilterOnlyInbound(t *testing.T) {
	db := testdb.Open(t)
	id := addPerson(t, db, "hal", "email", "")
	in := &storage.PersonMessage{PersonID: id, Source: "outlook", Subject: "in"}
	out := &storage.PersonMessage{PersonID: id, Source: "outlook", Subject: "out", Direction: "outbound"}
	for _, m := range []*storage.PersonMessage{in, out} {
		if err := db.UpsertPersonMessage(m, ""); err != nil {
			t.Fatal(err)
		}
	}
	unread, err := db.ListAllPersonMessagesFiltered("", "", true, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(unread) != 1 || unread[0].Subject != "in" || unread[0].ReadAt != nil {
		t.Fatalf("unread = %+v", unread)
	}
	if _, err := db.MarkPersonMessagesRead("", id, []string{in.ID}); err != nil {
		t.Fatal(err)
	}
	if unread, _ := db.ListAllPersonMessagesFiltered("", "", true, 10, 0); len(unread) != 0 {
		t.Errorf("unread after mark read = %d", len(unread))
	}
	all, _ := db.ListAllPersonMessagesFiltered("", "", false, 10, 0)
	if len(all) != 2 {
		t.Errorf("all = %d, want 2", len(all))
	}
}

// --- status updates ----------------------------------------------------------

func TestHealthPersonStatusUpdates(t *testing.T) {
	db := testdb.Open(t)
	id := addPerson(t, db, "ivy", "x", "")
	workID := addPerson(t, db, "ivy", "x", "work")

	if _, err := db.AddPersonStatusUpdate(id, "", "   "); err == nil {
		t.Error("blank status text should be rejected")
	}
	if _, err := db.AddPersonStatusUpdate(workID, "default", "x"); err == nil {
		t.Error("status for another profile's person should be rejected")
	}
	if u, err := db.GetLatestPersonStatusUpdate(id, ""); err != nil || u != nil {
		t.Errorf("latest with none = %v, %v", u, err)
	}
	for i := 0; i < 3; i++ {
		u, err := db.AddPersonStatusUpdate(id, "", fmt.Sprintf("  note %d ", i))
		if err != nil {
			t.Fatal(err)
		}
		if u.Text != fmt.Sprintf("note %d", i) {
			t.Errorf("text not trimmed: %q", u.Text)
		}
		time.Sleep(2 * time.Millisecond) // distinct created_at for ordering
	}
	latest, _ := db.GetLatestPersonStatusUpdate(id, "")
	if latest == nil || latest.Text != "note 2" {
		t.Errorf("latest = %+v", latest)
	}
	all, _ := db.ListPersonStatusUpdates(id, "", 0)
	if len(all) != 3 || all[0].Text != "note 2" || all[2].Text != "note 0" {
		t.Errorf("all newest first = %+v", all)
	}
	two, _ := db.ListPersonStatusUpdates(id, "", 2)
	if len(two) != 2 {
		t.Errorf("limit 2 = %d", len(two))
	}
	if other, _ := db.ListPersonStatusUpdates(id, "work", 0); len(other) != 0 {
		t.Errorf("work profile sees default's updates: %d", len(other))
	}
}

// --- social lists ------------------------------------------------------------

func TestHealthSocialListsAndItems(t *testing.T) {
	db := testdb.Open(t)
	l := &storage.SocialList{Name: "Leads", ListType: "people"}
	if err := db.CreateSocialList(l); err != nil {
		t.Fatal(err)
	}
	l2 := &storage.SocialList{Name: "Other"}
	if err := db.CreateSocialList(l2); err != nil {
		t.Fatal(err)
	}

	if err := db.AddSocialListItem(&storage.SocialListItem{ListID: l.ID, Platform: "x", PlatformUsername: "u1", URL: "https://x.example/u1", FollowerCount: 7}); err != nil {
		t.Fatal(err)
	}
	if err := db.BatchAddSocialListItems(nil); err != nil {
		t.Fatalf("empty batch: %v", err)
	}
	batch := []*storage.SocialListItem{
		{ListID: l.ID, Platform: "x", PlatformUsername: "u2", FullName: "U Two"},
		{ListID: l.ID, Platform: "x", PlatformUsername: "u3"},
		{ListID: l2.ID, Platform: "instagram", PlatformUsername: "v1"},
	}
	if err := db.BatchAddSocialListItems(batch); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetSocialList(l.ID)
	if err != nil || got == nil {
		t.Fatalf("GetSocialList: %v %v", got, err)
	}
	if got.ItemCount != 3 || got.ListType != "people" || got.Name != "Leads" {
		t.Errorf("list after adds = %+v", got)
	}
	if g2, _ := db.GetSocialList(l2.ID); g2.ItemCount != 1 || g2.ListType != "" {
		t.Errorf("second list = %+v", g2)
	}
	items, err := db.ListSocialListItems(l.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0].PlatformUsername != "u1" || items[0].URL != "https://x.example/u1" || items[0].FollowerCount != 7 {
		t.Errorf("items = %+v", items)
	}
	lists, _ := db.ListSocialLists()
	if len(lists) != 2 {
		t.Errorf("ListSocialLists = %d", len(lists))
	}

	// An item for a list that does not exist violates the foreign key and the
	// whole batch rolls back.
	bad := []*storage.SocialListItem{
		{ListID: l.ID, Platform: "x", PlatformUsername: "ok"},
		{ListID: "missing", Platform: "x", PlatformUsername: "bad"},
	}
	if err := db.BatchAddSocialListItems(bad); err == nil {
		t.Error("batch with an unknown list should fail")
	}
	if items, _ := db.ListSocialListItems(l.ID); len(items) != 3 {
		t.Errorf("failed batch left partial rows: %d", len(items))
	}
	if err := db.AddSocialListItem(&storage.SocialListItem{ListID: "missing", Platform: "x", PlatformUsername: "z"}); err == nil {
		t.Error("item for an unknown list should fail")
	}

	if err := db.DeleteSocialList(l.ID); err != nil {
		t.Fatal(err)
	}
	if items, _ := db.ListSocialListItems(l.ID); len(items) != 0 {
		t.Errorf("items survived list delete: %d", len(items))
	}
	if gone, err := db.GetSocialList(l.ID); err != nil || gone != nil {
		t.Errorf("GetSocialList after delete = %v, %v", gone, err)
	}
	if err := db.DeleteSocialList(l.ID); err == nil {
		t.Error("deleting a missing list should fail")
	}
}

// --- threads, templates, configs, settings -----------------------------------

func TestHealthThreadsUpsert(t *testing.T) {
	db := testdb.Open(t)
	th := &storage.Thread{SocialUserID: "u1", Platform: "x", Messages: `["hi"]`}
	if err := db.UpsertThread(th); err != nil {
		t.Fatal(err)
	}
	// Same (user, platform) updates in place.
	if err := db.UpsertThread(&storage.Thread{SocialUserID: "u1", Platform: "x", Messages: `["hi","yo"]`, Metadata: `{"a":1}`}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertThread(&storage.Thread{SocialUserID: "u1", Platform: "instagram"}); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetThread("u1", "x")
	if err != nil || got == nil {
		t.Fatalf("GetThread: %v %v", got, err)
	}
	if got.ID != th.ID || got.Messages != `["hi","yo"]` || got.Metadata != `{"a":1}` {
		t.Errorf("thread after upsert = %+v", got)
	}
	if missing, err := db.GetThread("nobody", "x"); err != nil || missing != nil {
		t.Errorf("GetThread missing = %v, %v", missing, err)
	}
	all, _ := db.ListThreads("")
	onX, _ := db.ListThreads("x")
	if len(all) != 2 || len(onX) != 1 {
		t.Errorf("threads all/x = %d/%d", len(all), len(onX))
	}
}

func TestHealthTemplatesCRUD(t *testing.T) {
	db := testdb.Open(t)
	b := &storage.Template{Name: "b-intro", Body: "Hi {{name}}", Subject: "Hello"}
	a := &storage.Template{Name: "a-follow", Body: "Ping"}
	for _, tmpl := range []*storage.Template{b, a} {
		if err := db.CreateTemplate(tmpl); err != nil {
			t.Fatal(err)
		}
	}
	if a.ID == 0 || b.ID == 0 || a.ID == b.ID {
		t.Fatalf("template ids = %d/%d", a.ID, b.ID)
	}
	got, err := db.GetTemplate(b.ID)
	if err != nil || got == nil || got.Subject != "Hello" || got.Body != "Hi {{name}}" {
		t.Errorf("GetTemplate = %+v, %v", got, err)
	}
	list, _ := db.ListTemplates()
	if len(list) != 2 || list[0].Name != "a-follow" {
		t.Errorf("templates by name = %+v", list)
	}
	if err := db.DeleteTemplate(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteTemplate(a.ID); err == nil {
		t.Error("deleting a missing template should fail")
	}
	if gone, err := db.GetTemplate(a.ID); err != nil || gone != nil {
		t.Errorf("GetTemplate after delete = %v, %v", gone, err)
	}
}

func TestHealthConfigsAndSettings(t *testing.T) {
	db := testdb.Open(t)
	if err := db.SaveConfig("zeta", `{"v":1}`); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveConfig("alpha", `{}`); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveConfig("zeta", `{"v":2}`); err != nil {
		t.Fatal(err)
	}
	c, err := db.GetConfig("zeta")
	if err != nil || c == nil || c.ConfigData != `{"v":2}` {
		t.Errorf("GetConfig = %+v, %v", c, err)
	}
	if missing, err := db.GetConfig("nope"); err != nil || missing != nil {
		t.Errorf("GetConfig missing = %v, %v", missing, err)
	}
	list, _ := db.ListConfigs()
	if len(list) != 2 || list[0].Name != "alpha" {
		t.Errorf("configs = %+v", list)
	}

	if _, err := db.GetSetting("health.k"); err == nil {
		t.Error("missing setting should error")
	}
	if err := db.SetSetting("health.k", "1"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting("health.k", "2"); err != nil {
		t.Fatal(err)
	}
	if v, err := db.GetSetting("health.k"); err != nil || v != "2" {
		t.Errorf("GetSetting = %q, %v", v, err)
	}
}

// --- person links ------------------------------------------------------------

func TestHealthPersonLinksLifecycle(t *testing.T) {
	db := testdb.Open(t)
	a := addPerson(t, db, "pa", "x", "")
	b := addPerson(t, db, "pb", "instagram", "")
	c := addPerson(t, db, "pc", "linkedin", "")

	if _, err := db.InsertPersonLink(&storage.PersonLink{PersonA: a, PersonB: a, Relation: storage.PersonLinkSame, Status: storage.PersonLinkSuggested}); err == nil {
		t.Error("self link should be rejected")
	}
	if _, err := db.InsertPersonLink(&storage.PersonLink{PersonA: a, PersonB: b, Relation: "maybe", Status: storage.PersonLinkSuggested}); err == nil {
		t.Error("bad relation should be rejected")
	}
	if _, err := db.InsertPersonLink(&storage.PersonLink{PersonA: a, PersonB: b, Relation: storage.PersonLinkSame, Status: "new"}); err == nil {
		t.Error("bad status should be rejected")
	}

	l := &storage.PersonLink{PersonA: b, PersonB: a, Relation: storage.PersonLinkSame, Status: storage.PersonLinkSuggested, Confidence: 0.8, Source: "jev"}
	ins, err := db.InsertPersonLink(l)
	if err != nil || !ins {
		t.Fatalf("InsertPersonLink = %v, %v", ins, err)
	}
	if l.PersonA > l.PersonB || l.ProfileID != "default" {
		t.Errorf("pair not ordered / profile not defaulted: %+v", l)
	}
	// The reversed pair is the same link and is left alone.
	if ins, err := db.InsertPersonLink(&storage.PersonLink{PersonA: a, PersonB: b, Relation: storage.PersonLinkNotSame, Status: storage.PersonLinkDismissed}); err != nil || ins {
		t.Errorf("duplicate pair insert = %v, %v; want false, nil", ins, err)
	}
	if _, err := db.InsertPersonLink(&storage.PersonLink{PersonA: a, PersonB: c, Relation: storage.PersonLinkSame, Status: storage.PersonLinkSuggested}); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ListPersonLinks("default", "bogus"); err == nil {
		t.Error("ListPersonLinks with a bad status should fail")
	}
	sugg, _ := db.ListPersonLinks("default", storage.PersonLinkSuggested)
	if len(sugg) != 2 {
		t.Errorf("suggested = %d", len(sugg))
	}
	if err := db.SetPersonLinkStatus("default", l.ID, storage.PersonLinkDismissed); err != nil {
		t.Fatal(err)
	}
	if err := db.SetPersonLinkStatus("work", l.ID, storage.PersonLinkConfirmed); !errors.Is(err, storage.ErrPersonLinkNotFound) {
		t.Errorf("other profile status change = %v, want ErrPersonLinkNotFound", err)
	}
	if err := db.SetPersonLinkStatus("default", l.ID, "x"); err == nil {
		t.Error("bad status should be rejected")
	}
	forA, _ := db.PersonLinksFor("default", a)
	if len(forA) != 2 {
		t.Fatalf("links for a = %d", len(forA))
	}
	for _, link := range forA {
		if link.ID == l.ID {
			if link.Status != storage.PersonLinkDismissed || link.Relation != storage.PersonLinkNotSame {
				t.Errorf("dismissed link = %+v", link)
			}
			if link.Other(a) != b || link.Other(b) != a {
				t.Errorf("Other() wrong for %+v", link)
			}
		}
	}
	if forC, _ := db.PersonLinksFor("work", c); len(forC) != 0 {
		t.Errorf("work profile sees default's links: %d", len(forC))
	}
}

// --- sessions ----------------------------------------------------------------

func TestHealthCleanExpiredSessionsAndNormalizePlatforms(t *testing.T) {
	db := testdb.Open(t)
	now := time.Now().UTC()
	ins := func(user, platform string, expiry time.Time) {
		if _, err := db.DB.Exec(`INSERT INTO crawler_sessions (username, platform, cookies_json, expiry) VALUES (?,?,?,?)`,
			user, platform, "", expiry); err != nil {
			t.Fatalf("insert session: %v", err)
		}
	}
	ins("old", "instagram", now.Add(-time.Hour))
	ins("fresh", "instagram", now.Add(time.Hour))
	ins("tw", "twitter", now.Add(time.Hour))

	if err := db.CleanExpiredSessions(); err != nil {
		t.Fatal(err)
	}
	if err := db.NormalizePlatformNames(); err != nil {
		t.Fatal(err)
	}
	rows, err := db.DB.Query(`SELECT username, platform FROM crawler_sessions ORDER BY username`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var u, p string
		if err := rows.Scan(&u, &p); err != nil {
			t.Fatal(err)
		}
		got = append(got, u+"/"+p)
	}
	if strings.Join(got, ",") != "fresh/instagram,tw/x" {
		t.Errorf("sessions = %v, want fresh/instagram,tw/x", got)
	}
}
