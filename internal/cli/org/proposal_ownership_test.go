package org

import (
	"strings"
	"testing"
)

// A grant proposal is made over a workflow of THIS profile only (#1477): a workflow owned by another profile or by none
// cannot be proposed a grant over, and an older pending proposal goes stale when its workflow stops being the profile's.

func TestAGrantProposalOverAWorkflowOfAnotherProfileOrNoProfileIsRefused(t *testing.T) {
	for name, owner := range map[string]string{"another profile": "someone-else", "no profile": ""} {
		t.Run(name, func(t *testing.T) {
			f := propFixture(t)
			f.sql(t, `UPDATE workflows SET profile_id = ? WHERE id = ?`, owner, f.plainWF)
			if _, err := f.run(t, "proposal", "create", "growth", "--verb", "grant.add", "--args-json", grantArgs); err == nil ||
				!strings.Contains(err.Error(), "cannot be read") {
				t.Fatalf("create over a workflow of %s: %v", name, err)
			}
			list := f.mustRun(t, "proposal", "list")["proposals"].([]interface{})
			if len(list) != 0 {
				t.Errorf("a refused create stored %d proposals", len(list))
			}
		})
	}
}

func TestAnOlderGrantProposalGoesStaleWhenItsWorkflowIsNoLongerTheProfiles(t *testing.T) {
	for name, owner := range map[string]string{"another profile": "someone-else", "no profile": ""} {
		t.Run(name, func(t *testing.T) {
			f := propFixture(t)
			p := f.propose(t, "growth", "grant.add", grantArgs)
			f.sql(t, `UPDATE workflows SET profile_id = ? WHERE id = ?`, owner, f.plainWF)
			if _, err := f.confirm(t, p); err == nil || !strings.Contains(err.Error(), "proposal_stale") {
				t.Fatalf("confirm after the workflow went to %s: %v", name, err)
			}
			if len(f.grants(t)) != 0 {
				t.Error("a stale proposal granted anyway")
			}
		})
	}
}
