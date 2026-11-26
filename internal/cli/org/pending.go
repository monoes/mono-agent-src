package org

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/orgchat"
	"github.com/monoes/mono-agent/internal/orgdesign"
)

// pendingItem is one thing an org waits on a person for.
type pendingItem struct {
	Org string `json:"org"`
	orgchat.Item
	// RequestID names an approval's request for `org approve|deny --request`;
	// empty from a monomind that records none, and then the request cannot be
	// resolved on its own.
	RequestID string `json:"request_id,omitempty"`
	// Input is the call's arguments, redacted and cut by monomind.
	Input map[string]interface{} `json:"input,omitempty"`
}

type pendingError struct {
	Org   string `json:"org"`
	Error string `json:"error"`
}

// Bounds of `org pending`: orgs read at once, and the time one org may take.
const (
	pendingParallel = 4
	pendingTimeout  = 15 * time.Second
)

func newOrgPendingCmd(env *orgEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "pending",
		Short: "Every question, approval and gate the folder's orgs wait on a person for (JSON)",
		Long: "Items of every org, oldest first per org. An approval carries request_id (for `org approve|deny " +
			"--request`), input, and for a shell call command, command_truncated and cwd. An org that cannot " +
			"be read is listed under errors.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root := env.root()
			names, err := orgdesign.ListOrgNames(root)
			if err != nil {
				return err
			}
			items := make([][]pendingItem, len(names))
			failed := make([][]pendingError, len(names))
			slots := make(chan struct{}, pendingParallel)
			var wg sync.WaitGroup
			for i, name := range names {
				wg.Add(1)
				slots <- struct{}{}
				go func(i int, name string) {
					defer wg.Done()
					defer func() { <-slots }()
					ctx, cancel := context.WithTimeout(cmd.Context(), pendingTimeout)
					defer cancel()
					items[i], failed[i] = orgPending(ctx, root, name)
				}(i, name)
			}
			wg.Wait()
			all, errs := []pendingItem{}, []pendingError{}
			for i := range names {
				all = append(all, items[i]...)
				errs = append(errs, failed[i]...)
			}
			return printJSONValue(map[string]interface{}{"v": 1, "items": all, "errors": errs})
		},
	}
}

// orgPending reads one org's three item lists at once. A list that fails is
// reported and the others still show.
func orgPending(ctx context.Context, root, org string) ([]pendingItem, []pendingError) {
	var human orgchat.HumanItems
	var failed [3]error
	var wg sync.WaitGroup
	kinds := [3]string{"questions", "approvals", "gates"}
	// Each parser fills its own field of human.
	parsers := [3]func(raw []byte) error{
		func(raw []byte) (err error) { human.Questions, err = orgchat.ParseQuestions(raw); return },
		func(raw []byte) (err error) { human.Approvals, err = orgchat.ParseApprovals(raw); return },
		func(raw []byte) (err error) { human.Gates, err = orgchat.ParseGates(raw); return },
	}
	for i := range kinds {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			raw, err := orgChatClient.All(ctx, root, org, kinds[i])
			if err == nil {
				err = parsers[i](raw)
			}
			failed[i] = err
		}(i)
	}
	wg.Wait()
	var errs []pendingError
	for i, err := range failed {
		if err != nil {
			errs = append(errs, pendingError{Org: org, Error: kinds[i] + ": " + err.Error()})
		}
	}

	byID := map[string]orgchat.Approval{}
	for _, a := range human.Approvals {
		if a.RequestID != nil && *a.RequestID != "" {
			byID[*a.RequestID] = a
		}
	}
	folder := roleFolders(root, org)
	var out []pendingItem
	// A thread of no events is the lists alone, in the shape the chat shows.
	for _, it := range orgchat.BuildThread(org, "", nil, human, 0) {
		if !it.Pending {
			continue
		}
		p := pendingItem{Org: org, Item: it}
		if a, ok := byID[it.Ref]; ok && it.Kind == orgchat.ItemApproval {
			p.RequestID, p.Input = it.Ref, a.Input
		}
		if p.Command != "" {
			p.Cwd = folder(p.Role)
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out, errs
}
