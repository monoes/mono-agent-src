package main

import (
	"fmt"
	"regexp"
)

// The three bindings of an org proposal card (#1477): the card reads the stored proposal and the user's click confirms
// or declines it. All the logic lives in `monoagentcli org proposal show|confirm|decline`; these only pass an id (and,
// for confirm, the hash the card displayed) to the bundled CLI. The app never builds the grant or autonomy command: it
// is the CLI that re-runs it from the stored arguments, and only after its own gate and the hash check.
//
// In `wails dev` any same-user process can call any bound method; a production webview cannot be reached that way.
// The gate that matters is the CLI's (confirm is operator-only), and these methods add no authority of their own.

// proposalIDPattern is the id a proposal has (128 random bits, hex); proposalHashPattern its hash (sha256, hex).
var (
	proposalIDPattern   = regexp.MustCompile(`^[0-9a-f]{32}$`)
	proposalHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func proposalIDArg(id string) error {
	if !proposalIDPattern.MatchString(id) {
		return fmt.Errorf("invalid proposal id")
	}
	return nil
}

func proposalShowArgs(id string) ([]string, error) {
	if err := proposalIDArg(id); err != nil {
		return nil, err
	}
	return []string{"proposal", "show", id}, nil
}

func proposalDeclineArgs(id string) ([]string, error) {
	if err := proposalIDArg(id); err != nil {
		return nil, err
	}
	return []string{"proposal", "decline", id}, nil
}

// proposalConfirmArgs passes the hash the card displayed, so a row rewritten after it was drawn is refused by the CLI.
func proposalConfirmArgs(id, hash string) ([]string, error) {
	if err := proposalIDArg(id); err != nil {
		return nil, err
	}
	if !proposalHashPattern.MatchString(hash) {
		return nil, fmt.Errorf("invalid proposal hash")
	}
	return []string{"proposal", "confirm", id, "--hash", hash}, nil
}

// GetOrgProposal reads one stored proposal (`org proposal show`): the card shows its preview text and hash.
func (a *App) GetOrgProposal(id string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	sub, err := proposalShowArgs(id)
	return a.runOrgSub("proposal show", sub, err)
}

// ConfirmOrgProposal applies the proposal the user saw, identified by id and the hash the card displayed.
func (a *App) ConfirmOrgProposal(id, hash string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	sub, err := proposalConfirmArgs(id, hash)
	return a.runOrgSub("proposal confirm", sub, err)
}

// DeclineOrgProposal ends a pending proposal without applying it.
func (a *App) DeclineOrgProposal(id string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	sub, err := proposalDeclineArgs(id)
	return a.runOrgSub("proposal decline", sub, err)
}
