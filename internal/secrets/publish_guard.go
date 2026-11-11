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
	n, _ := ctx.Value(accessNodeKey{}).(accessNode)
	name := nodeType
	if n.name != "" {
		name = fmt.Sprintf("%q (%s)", n.name, nodeType)
	}
	return fmt.Errorf("%w: %s publishes to a third party and this run has read a secret under agent access, so it does not run; "+
		"the person can approve the workflow to run it", ErrPublishesSecret, name)
}

// CheckSendContent refuses a request whose content (its body: what the
// destination stores or shows) holds a secret read under agent access. The
// auth surfaces (Authorization and other headers, a credential in the query)
// are not content and are not passed here. A destination on this machine
// is exempt: the value does not leave it.
func CheckSendContent(ctx context.Context, ownMachine bool, content ...string) error {
	a := accessOf(ctx)
	if a == nil || ownMachine {
		return nil
	}
	for _, v := range a.values() {
		for _, t := range content {
			if strings.Contains(t, v) {
				return fmt.Errorf("%w: the request's body holds a secret; a granted secret goes to a host only as authentication", ErrPublishesSecret)
			}
		}
	}
	return nil
}
