package task

import (
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/storage"
)

// storedStamp is the one format the board writes a time in.
const storedStamp = "2006-01-02T15:04:05Z"

// taskSeed is a tasks row written straight into the table, around the store: the
// commands that bring a card to In progress, Review or Done belong to later tasks.
type taskSeed struct {
	profile   string // "" is default
	title     string
	notes     string
	status    string // "" is inbox
	source    string // "" is cli
	link      string
	pageTitle string
	app       string
	holder    string    // claimed_by
	until     time.Time // claim_until, for a holder
	created   time.Time // zero is now
}

// seedTaskRows writes the rows in one transaction, in order, each below the cards
// before it in its column, and returns their ids.
func seedTaskRows(t *testing.T, dbPath string, rows ...taskSeed) []int64 {
	t.Helper()
	raw, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	tx, err := raw.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		if r.profile == "" {
			r.profile = "default"
		}
		if r.status == "" {
			r.status = "inbox"
		}
		if r.source == "" {
			r.source = "cli"
		}
		if r.created.IsZero() {
			r.created = time.Now()
		}
		until := ""
		if r.holder != "" {
			until = r.until.UTC().Format(storedStamp)
		}
		created := r.created.UTC().Format(storedStamp)
		res, err := tx.Exec(`INSERT INTO tasks (profile_id, title, notes, status, position, source_kind, source_url, source_title, source_app, claimed_by, claim_until, created_at, updated_at)
			VALUES (?, ?, ?, ?, (SELECT COALESCE(MAX(position), 0) + 1 FROM tasks), ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.profile, r.title, r.notes, r.status, r.source, r.link, r.pageTitle, r.app, r.holder, until, created, created)
		if err != nil {
			t.Fatal(err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return ids
}

// seedTaskEvent writes a row of a task's history, around the store.
func seedTaskEvent(t *testing.T, dbPath string, taskID int64, at time.Time, actor, kind, from, to, note string) {
	t.Helper()
	raw, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.DB.Exec(`INSERT INTO task_events (task_id, at, actor, kind, from_status, to_status, note) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		taskID, at.UTC().Format(storedStamp), actor, kind, from, to, note); err != nil {
		t.Fatal(err)
	}
}

// addTaskProfile inserts a profile besides the bootstrapped default.
func addTaskProfile(t *testing.T, dbPath, id, name string) {
	t.Helper()
	raw, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.DB.Exec(`INSERT INTO profiles (id, name) VALUES (?, ?)`, id, name); err != nil {
		t.Fatal(err)
	}
}

func titlesOf(l listJSON) []string {
	titles := make([]string, 0, len(l.Tasks))
	for _, task := range l.Tasks {
		titles = append(titles, task.Title)
	}
	return titles
}

// heldJSON is the part of a task document that says who holds it.
type heldJSON struct {
	Tasks []struct {
		Title string `json:"title"`
		Claim *struct {
			By    string `json:"by"`
			Stale bool   `json:"stale"`
		} `json:"claim"`
	} `json:"tasks"`
}

// setLocalZone makes zone the local time zone until the test ends. A test that
// checks a clock time sets one that is not UTC and writes what it expects with
// In(zone), never with Local(): then a command that writes UTC where it means
// local time fails the test, on a runner whose own zone is UTC as well.
func setLocalZone(t *testing.T, zone *time.Location) {
	t.Helper()
	old := time.Local
	time.Local = zone
	t.Cleanup(func() { time.Local = old })
}

// localNoonZone sets a local zone that is not UTC and whose clock reads about 12:00
// right now, so that a lease an hour from now ends today and one 30 hours from now ends
// on another day, whenever the test runs. It returns the zone.
func localNoonZone(t *testing.T) *time.Location {
	t.Helper()
	now := time.Now().UTC()
	offset := 12*3600 - (now.Hour()*3600 + now.Minute()*60 + now.Second())
	if offset%1800 == 0 { // never a zone of whole or half hours, whenever the test runs
		offset += 7*60 + 13
	}
	zone := time.FixedZone("Noon", offset)
	setLocalZone(t, zone)
	return zone
}
