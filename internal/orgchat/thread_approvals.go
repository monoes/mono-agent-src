package orgchat

// mergeApprovals folds the approvals list, the record of what is being
// approved, into the thread. Each listed call has an item of its own, and all
// an item says about a call (text, command, input, ref, request id, state)
// comes from one call.
//
// An item of a bus event with a request id is that call's. Items of events
// without one can't be told apart, so a listed call takes the place of the
// next such item of its role and action and replaces everything in it. The
// ones left over repeat a call already shown and stop waiting for a decision.
func (b *builder) mergeApprovals(list []Approval) {
	listed := map[string]bool{}
	for _, a := range list {
		ref := approvalRef(a)
		pair := a.RoleID + ":" + a.Action
		listed[pair] = true
		i, ok := b.byRef[ItemApproval+"\x00"+ref]
		switch {
		case ok:
		case len(b.unnamed[pair]) > 0:
			i, b.unnamed[pair] = b.unnamed[pair][0], b.unnamed[pair][1:]
			old := b.items[i]
			b.items[i] = Item{ID: old.ID, TS: old.TS, Kind: ItemApproval, Role: a.RoleID, Action: a.Action, Text: a.Question}
		case a.Approved != nil:
			continue
		default:
			i = b.add(Item{ID: ref, TS: a.TS, Kind: ItemApproval, Role: a.RoleID, Action: a.Action, Text: a.Question})
			b.byRef[ItemApproval+"\x00"+ref] = i
		}
		it := &b.items[i]
		it.Ref = ref
		it.RequestID = ""
		if a.RequestID != nil {
			it.RequestID = *a.RequestID
		}
		it.Pending = a.Approved == nil
		it.Resolution = approvalState(a)
		if command, truncated := Command(a.Input); command != "" {
			it.Command, it.CommandTruncated = command, truncated
		}
		if a.Input != nil {
			it.Input = a.Input
		}
		if a.ResolvedBy != nil {
			it.ResolvedBy = *a.ResolvedBy
		}
	}
	for pair, items := range b.unnamed {
		if listed[pair] {
			for _, i := range items {
				b.items[i].Pending = false
			}
		}
	}
}
