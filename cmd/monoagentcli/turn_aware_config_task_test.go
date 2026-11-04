package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/storage"
)

// Two decisions that used to read the markers alone, held to the turn-aware gate (opgate.Refusal): the
// storage layer's write guard for the reserved configs rows, and who a task's author is recorded as.
// Every context opgate refuses (a marked agent, the chat, a workflow, a process in a coder turn that
// cleared its markers, one in a turn of a registered host, a token holder, one that cannot be placed)
// gets the agent's answer; the operator's terminal is unchanged.

func turnAwareFixture(t *testing.T) *globalConfig {
	t.Helper()
	cfg := reservedGateFixture(t, false)
	return cfg
}

func TestStorageConfigGuardRefusesEveryContextOpgateRefuses(t *testing.T) {
	for _, c := range revealGateContexts() {
		t.Run(c.name, func(t *testing.T) {
			cfg := turnAwareFixture(t)
			c.setup(t, cfg)
			db, err := storage.NewDatabase(cfg.DBPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.DB.Close()
			err = db.SaveConfig("memory.runs", "off")
			if err == nil || !strings.Contains(err.Error(), "reserved setting") {
				t.Fatalf("a reserved row was written by %s: %v", c.name, err)
			}
			if e, _ := db.GetConfig("memory.runs"); e != nil {
				t.Fatalf("the refused write left a row: %+v", e)
			}
			// an ordinary row never asks the turn check: it passes in every context
			if err := db.SaveConfig("linkedin_login_x", "{}"); err != nil {
				t.Fatalf("an ordinary row: %v", err)
			}
		})
	}
}

func TestStorageConfigGuardOperatorIsUnchanged(t *testing.T) {
	cfg := turnAwareFixture(t)
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	if err := db.SaveConfig("memory.runs", "on"); err != nil {
		t.Fatalf("the operator's write: %v", err)
	}
	if e, _ := db.GetConfig("memory.runs"); e == nil || e.ConfigData != "on" {
		t.Fatalf("row: %+v", e)
	}
}

// The guard's text for a marked caller is the one it always gave.
func TestStorageConfigGuardMarkedTextIsUnchanged(t *testing.T) {
	cfg := turnAwareFixture(t)
	revealGateContexts()[0].setup(t, cfg)
	err := storage.ConfigWriteGuard("memory.runs")
	if err == nil || err.Error() != "config \"memory.runs\" is a reserved setting (it changes what agents may do or what runs unattended): only the operator writes it, with `monoagentcli config set` or the command made for it in their own terminal" {
		t.Fatalf("text: %v", err)
	}
}

type taskAddedDoc struct {
	Task struct {
		Author string `json:"author"`
		Source struct {
			Kind string `json:"kind"`
		} `json:"source"`
	} `json:"task"`
}

// A task added from any context opgate refuses is recorded as an agent's, as a marked agent's is.
func TestTaskProvenanceIsTheAgentsInEveryContextOpgateRefuses(t *testing.T) {
	for _, c := range revealGateContexts() {
		t.Run(c.name, func(t *testing.T) {
			cfg := turnAwareFixture(t)
			c.setup(t, cfg)
			out, err := runSendCLI(t, cfg, newTaskCmd(cfg), "add", "from", c.name)
			if err != nil {
				t.Fatalf("add: %v", err)
			}
			var doc taskAddedDoc
			if err := json.Unmarshal([]byte(out), &doc); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			if doc.Task.Source.Kind != "agent" || doc.Task.Author == "you" {
				t.Fatalf("recorded %+v, want an agent's author", doc.Task)
			}
		})
	}
}

func TestTaskProvenanceOperatorIsUnchanged(t *testing.T) {
	cfg := turnAwareFixture(t)
	out, err := runSendCLI(t, cfg, newTaskCmd(cfg), "add", "mine")
	if err != nil {
		t.Fatal(err)
	}
	var doc taskAddedDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Task.Author != "you" || doc.Task.Source.Kind != "cli" {
		t.Fatalf("the operator's task: %+v", doc.Task)
	}
}

// The limit a task command applies to a caller that names no agent is the turn-aware one.
func TestTaskLimitsRefuseEveryContextOpgateRefuses(t *testing.T) {
	for _, c := range revealGateContexts() {
		t.Run(c.name, func(t *testing.T) {
			cfg := turnAwareFixture(t)
			c.setup(t, cfg)
			if _, err := taskDeps(cfg).CallerFor("").Operator("do this"); err == nil {
				t.Fatalf("%s got the operator's limits", c.name)
			}
		})
	}
	cfg := turnAwareFixture(t)
	if _, err := taskDeps(cfg).CallerFor("").Operator("do this"); err != nil {
		t.Fatalf("the operator: %v", err)
	}
}

// With no turn check registered (the app, the daemon, library use) the decisions stay marker-only.
func TestTurnAwareDecisionsWithoutARegisteredGateAreMarkerOnly(t *testing.T) {
	needAncestry(t)
	cfg := turnAwareFixture(t)
	markThisProcessAsACoderTurn(t, cfg)
	opgate.SetForTest(t, nil)
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	if err := db.SaveConfig("memory.runs", "on"); err != nil {
		t.Fatalf("documented: no registered gate means the marker alone decides: %v", err)
	}
	if _, err := taskDeps(cfg).CallerFor("").Operator("do this"); err != nil {
		t.Fatal(err)
	}
}
