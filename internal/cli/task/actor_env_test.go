package task

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// A MONOAGENT_ACTOR that is set to a value of only spaces is what an --as that is given and blank is: an
// agent that has not said its name, never the operator. The two ways of naming an agent agree on the
// accident the rule is for (--as "$NAME", NAME unset, or a runner that exports a blank name). An empty
// value is no value, as the markers are read, and a name that is given wins over a blank one.
func TestTaskCallerReadsABlankMONOAGENTACTORAsAnAgentWithoutAName(t *testing.T) {
	t.Parallel()
	newTaskTestDB(t) // the operator's environment
	for _, blank := range []string{" ", "   ", "\t", " \t\n", "\U000000a0"} {
		setenv(t, "MONOAGENT_ACTOR", blank)
		c := parsedCaller(t)
		if !c.isAgent() || c.actor.Name != "" || c.marker != "" || c.asBlank || !c.envBlank {
			t.Errorf("MONOAGENT_ACTOR %q: %+v, want an agent with no name that is blank by the variable", blank, c)
			continue
		}
		_, err := c.operator("approve a task")
		var fields jsonErrorFields
		if exitCode(err) != 3 || !errors.As(err, &fields) || fields.JSONErrorFields()["code"] != "operator_only" ||
			!strings.Contains(errText(err), "MONOAGENT_ACTOR is set to a blank value, which counts as an agent") {
			t.Errorf("MONOAGENT_ACTOR %q as the operator: %v", blank, err)
		}
		_, err = c.agent()
		if exitCode(err) != 3 || jsonErrorCode(err) != "invalid_input" || !strings.Contains(errText(err), "MONOAGENT_ACTOR is set to a blank value") || !strings.Contains(errText(err), "--as NAME") {
			t.Errorf("MONOAGENT_ACTOR %q as an agent: %v", blank, err)
		}
	}
	setenv(t, "MONOAGENT_ACTOR", "   ")
	// With an agent-context marker as well, the refusal for the operator names both.
	setenv(t, "CLAUDECODE", "1")
	both := parsedCaller(t)
	if _, err := both.operator("approve a task"); !strings.Contains(errText(err), "CLAUDECODE") || !strings.Contains(errText(err), "MONOAGENT_ACTOR is set to a blank value") {
		t.Errorf("a marker and a blank MONOAGENT_ACTOR, as the operator: %v", err)
	}
	if _, err := both.agent(); !strings.Contains(errText(err), "MONOAGENT_ACTOR is set to a blank value") {
		t.Errorf("a marker and a blank MONOAGENT_ACTOR, as an agent: %v", err)
	}
	setenv(t, "CLAUDECODE", "")
	// A name that is given wins over the blank variable, which then says nothing.
	if c := parsedCaller(t, "--as", "bob"); c.envBlank || c.asBlank {
		t.Errorf("--as bob with a blank MONOAGENT_ACTOR: %+v", c)
	}
	if a, err := parsedCaller(t, "--as", " bob ").agent(); err != nil || a.Name != "bob" {
		t.Errorf("--as bob with a blank MONOAGENT_ACTOR: %+v, %v", a, err)
	}
	// A blank --as and a blank variable are blank twice: the refusal speaks of --as, which is what was typed.
	twice := parsedCaller(t, "--as", "")
	if !twice.isAgent() || !twice.asBlank {
		t.Errorf("a blank --as and a blank MONOAGENT_ACTOR: %+v", twice)
	}
	if _, err := twice.operator("approve a task"); !strings.Contains(errText(err), "--as is given with no name") {
		t.Errorf("a blank --as and a blank MONOAGENT_ACTOR, as the operator: %v", err)
	}
	if _, err := twice.agent(); !strings.Contains(errText(err), "--as needs a name") {
		t.Errorf("a blank --as and a blank MONOAGENT_ACTOR, as an agent: %v", err)
	}
	// An empty value is the same as none: the operator.
	setenv(t, "MONOAGENT_ACTOR", "")
	if c := parsedCaller(t); c.isAgent() || c.envBlank {
		t.Errorf("an empty MONOAGENT_ACTOR: %+v, want the operator", c)
	}
}

// The same on the commands: nothing the operator alone may do runs under a blank MONOAGENT_ACTOR, and the
// refusal names the variable; the agent's commands ask for a name and write nothing; what an agent may do
// without a name is its own (it does not see the Inbox); and a name that is given is the agent's.
func TestTaskABlankMONOAGENTACTORIsNeverTheOperatorOnAnyCommand(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	opsAdd(t, db, "in the inbox")
	n := opsAdd(t, db, "waiting", "--ready")
	refused := append(slices.Clone(operatorCommands), []string{"board"}, []string{"add", "x", "--ready"})
	for _, blank := range []string{"   ", "\t"} {
		t.Run(fmt.Sprintf("%q", blank), func(t *testing.T) {
			setenv(t, "MONOAGENT_ACTOR", blank)
			for _, c := range refused {
				doc := failedTaskJSON(t, db, "default", 3, c...)
				if msg, _ := doc["error"].(string); doc["code"] != "operator_only" || !strings.Contains(msg, "MONOAGENT_ACTOR is set to a blank value") {
					t.Errorf("task %s: %v, want operator_only, naming MONOAGENT_ACTOR", strings.Join(c, " "), doc)
				}
			}
			before := agentSnapshot(t, db)
			for _, verb := range agentVerbsFor(id(n)) {
				exit, code, msg := agentRefusal(t, db, verb...)
				if exit != 3 || code != "invalid_input" || !strings.Contains(msg, "MONOAGENT_ACTOR is set to a blank value") || !strings.Contains(msg, "--as NAME") {
					t.Errorf("task %s: exit %d, %q, %.200q; want exit 3, invalid_input, naming MONOAGENT_ACTOR and the way to choose a name (--as NAME)", strings.Join(verb, " "), exit, code, msg)
				}
			}
			if after := agentSnapshot(t, db); after != before {
				t.Errorf("refused commands changed the board:\nbefore\n%s\nafter\n%s", before, after)
			}
			var listed listJSON
			mustTaskJSON(t, db, "default", &listed, "", "list")
			if got := strings.Join(titlesOf(listed), "|"); got != "waiting" {
				t.Errorf("an agent without a name lists %q, want only the ready one", got)
			}
			// With a marker as well the refusal names the two.
			setenv(t, "CLAUDECODE", "1")
			doc := failedTaskJSON(t, db, "default", 3, "board")
			if msg, _ := doc["error"].(string); doc["code"] != "operator_only" || !strings.Contains(msg, "CLAUDECODE is set and MONOAGENT_ACTOR is set to a blank value") {
				t.Errorf("task board with a marker and a blank MONOAGENT_ACTOR: %v", doc)
			}
		})
	}
	// A name that is given is the agent's, whatever the variable holds.
	setenv(t, "MONOAGENT_ACTOR", "   ")
	if task := agentRun(t, db, "claim", id(n), "--as", "bob"); task.Claim == nil || task.Claim.By != "bob" {
		t.Errorf("--as bob with a blank MONOAGENT_ACTOR claimed %+v, want a task held by bob", task)
	}
	// An empty variable is the operator's terminal again.
	setenv(t, "MONOAGENT_ACTOR", "")
	if _, _, err := runTask(t, db, "default", true, "", "board"); err != nil {
		t.Errorf("task board with an empty MONOAGENT_ACTOR: %v", err)
	}
}
