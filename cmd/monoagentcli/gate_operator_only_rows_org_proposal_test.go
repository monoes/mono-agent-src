package main

// operatorOnlyRowsOrgProposal are the two verbs that decide an org proposal (#1477): confirming applies a grant or
// an autonomy change through the real command, declining ends the proposal. Both ask the gate before anything
// else, the id and the hash only reach it afterwards; neither is a recovery verb. create, list and show are
// agent-allowed (openOrgLeaves).
func operatorOnlyRowsOrgProposal() []opRow {
	return []opRow{
		{path: "org proposal confirm", build: newOrgCmd, args: []string{"proposal", "confirm", "00000000000000000000000000000000", "--hash", "x"}},
		{path: "org proposal decline", build: newOrgCmd, args: []string{"proposal", "decline", "00000000000000000000000000000000"}},
	}
}
