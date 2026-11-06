package org

import (
	"context"
	"strings"
	"testing"
)

// A proposal the chat tools store through CreateProposal is the proposal `org proposal create` stores: same preview,
// same hash for the same arguments, labelled by the gate (never "operator"), and the operator's confirm applies it.
func TestCreateProposalIsTheProposalTheCommandMakes(t *testing.T) {
	f := propFixture(t)
	env := testEnv(f.cfg)
	db, profileID, root, err := env.Profile()
	if err != nil {
		t.Fatal(err)
	}
	viaCmd := f.propose(t, "growth", "grant.add", grantArgs, "--conversation", "conv-9")
	p, err := CreateProposal(context.Background(), ProposalRequest{DB: db, ProfileID: profileID, Root: root, OpenWorkflowStore: env.OpenWorkflowStore,
		Org: "growth", Verb: "grant.add", ArgsJSON: grantArgs, Conversation: "conv-9"})
	if err != nil {
		t.Fatal(err)
	}
	if p["preview_text"] != viaCmd["preview_text"] || p["status"] != "pending" || p["created_by"] == "operator" {
		t.Fatalf("api %v\ncommand %v", p, viaCmd)
	}
	if len(f.grants(t)) != 0 {
		t.Fatal("creating applied the grant")
	}
	if _, err := f.confirm(t, p); err != nil {
		t.Fatalf("the operator's confirm of an API-made proposal: %v", err)
	}
	if len(f.grants(t)) != 1 {
		t.Errorf("confirm applied %d grants", len(f.grants(t)))
	}
}

func TestCreateProposalRefusesWhatTheCommandRefuses(t *testing.T) {
	f := propFixture(t)
	env := testEnv(f.cfg)
	db, profileID, root, err := env.Profile()
	if err != nil {
		t.Fatal(err)
	}
	req := ProposalRequest{DB: db, ProfileID: profileID, Root: root, OpenWorkflowStore: env.OpenWorkflowStore, Org: "growth", Verb: "grant.add"}
	for _, c := range []struct{ org, verb, args, want string }{
		{"growth", "grant.add", `{"role":"lead","automation":"summarize","approval":"none"}`, "always asks first"},
		{"growth", "autonomy.set", `{"level":"full"}`, "level full"},
		{"growth", "autonomy.set", `{"policy":"x"}`, "not valid"},
		{"growth", "grant.remove", `{}`, "not one a proposal can carry"},
		{"nowhere", "grant.add", grantArgs, "not found"},
	} {
		r := req
		r.Org, r.Verb, r.ArgsJSON = c.org, c.verb, c.args
		if _, err := CreateProposal(context.Background(), r); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c, err, c.want)
		}
	}
	if _, err := CreateProposal(context.Background(), ProposalRequest{}); err == nil {
		t.Error("an empty request was accepted")
	}
}
