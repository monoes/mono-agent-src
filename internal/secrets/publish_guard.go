package secrets

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrPublishesSecret is wrapped by the refusals of CheckPublish and
// CheckSendContent: a secret read under agent access (a grant) goes to
// auth, never out as content. Host binding says where a secret may go,
// not what it is used as (issue #1418).
var ErrPublishesSecret = errors.New("a granted secret from the vault is not published as content")

// CheckPublish refuses to run a node that publishes to third parties
// (publishes: its executor does not declare PublishesToThirdParty() false)
// in a run under agent access that has read a secret. The run is tainted as a
// whole: data moves between steps in forms nobody can list, so the content is
// not scanned, the run is. A run under no agent access (the user's own, or an
// approved workflow) is not checked. The nodes that hold their own sends to the
// grant's hosts and keep a secret out of their content (CheckSendContent)
// are not refused here: they are the way a granted secret is used.
func CheckPublish(ctx context.Context, nodeType string, publishes bool) error {
	a := accessOf(ctx)
	if a == nil || !publishes || hostCheckedNodes[nodeType] {
		return nil
	}
	a.mu.Lock()
	read := a.read
	a.mu.Unlock()
	if !read {
		return nil
	}
	return fmt.Errorf("%w: %s publishes to a third party and this run has read a granted secret, so it does not run; %s",
		ErrPublishesSecret, a.who(ctx, nodeType), a.wayOut())
}

// who names the step ctx runs for a message: its name and type.
func (a *accessState) who(ctx context.Context, nodeType string) string {
	n, _ := ctx.Value(accessNodeKey{}).(accessNode)
	if nodeType == "" {
		nodeType = n.typ
	}
	switch {
	case n.name != "" && nodeType != "":
		return fmt.Sprintf("%q (%s)", n.name, nodeType)
	case nodeType != "":
		return nodeType
	}
	return "this step"
}

// wayOut is how a person lets the step run: approving the workflow, or, for
// a step run by itself, which has none, putting it in one.
func (a *accessState) wayOut() string {
	if a.origin.WorkflowID != "" {
		return "the person can approve the workflow to run it"
	}
	return "this step belongs to no workflow: put it in a workflow the person approves, or have the person run it"
}

// CheckSendContent refuses a request whose content (what the destination
// stores or shows: its body, a query parameter's name) holds a secret read
// under agent access. field says where, for the message. The auth surfaces
// (Authorization and other headers, a credential's value in the query) are
// not content and are not passed here. A destination on this machine is
// exempt: the value does not leave it. A caller checks UnderAgentAccess
// first, so a request outside agent access never builds content.
func CheckSendContent(ctx context.Context, ownMachine bool, field string, content ...string) error {
	a := accessOf(ctx)
	if a == nil || ownMachine {
		return nil
	}
	for _, v := range a.values() {
		for _, t := range content {
			if strings.Contains(t, v) {
				return fmt.Errorf("%w: %s sends a granted secret in its %s, which would publish it; a granted secret goes to a host only as authentication; %s",
					ErrPublishesSecret, a.who(ctx, ""), field, a.wayOut())
			}
		}
	}
	return nil
}

// TaintResolved makes ctx's run count names as read (a child run's reads,
// handed back with its items): the run is tainted, and each value that can
// still be read is tracked for masking and the content check. Unlike
// SeedResolved it asks nothing of the grant: it only adds restrictions.
func TaintResolved(ctx context.Context, names []string) {
	a := accessOf(ctx)
	if a == nil {
		return
	}
	for _, name := range names {
		if v, err := resolveEntry(ctx, a.db, a.profileID, secretRefPrefix+name, name); err == nil {
			a.track(name, v)
			continue
		}
		a.mu.Lock()
		a.names[name] = struct{}{}
		a.read = true
		a.mu.Unlock()
	}
}

// StoreField is a text a store writer is given, and what it is called in a
// refusal.
type StoreField struct{ Name, Text string }

// CheckStoreWrite refuses a write of a secret a run under agent access has
// read into a local store another run can read back (a table, a file, a
// record): the taint is per run, so run 2 would start clean and publish what
// run 1 stored (issue #1418). fields are what the node is given to write. The
// message names the field and the secret (a name the run itself read, never
// its value). A caller checks UnderAgentAccess first, so no text is built
// outside it.
func CheckStoreWrite(ctx context.Context, nodeType string, fields ...StoreField) error {
	a := accessOf(ctx)
	if a == nil {
		return nil
	}
	for _, v := range a.values() {
		for _, f := range fields {
			if strings.Contains(f.Text, v) {
				a.mu.Lock()
				owner := a.owner[v]
				a.mu.Unlock()
				return fmt.Errorf("%w: %s would store the value of the granted secret %q in its %s, where another run can read it back and publish it; "+
					"if this is a coincidence (a short secret inside a longer number, a timestamp), %s",
					ErrPublishesSecret, a.who(ctx, nodeType), owner, f.Name, a.wayOut())
			}
		}
	}
	return nil
}

// RunHasRead reports whether the run under agent access has read a secret:
// the cheap test that lets a store writer skip building the texts to scan.
func RunHasRead(ctx context.Context) bool {
	a := accessOf(ctx)
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.read
}
