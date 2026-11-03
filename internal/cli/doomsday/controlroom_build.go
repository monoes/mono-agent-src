package doomsday

import (
	"encoding/hex"
	"fmt"

	"github.com/monoes/mono-agent/internal/doomsday/store"
)

const headPrefixBytes = 8 // 16 hex digits, as `status` shows

func buildIntegrity(c store.ChainState) *IntegrityInfo {
	in := &IntegrityInfo{ChainVerified: VerifiedYes, HeadSeq: c.HeadSeq}
	if len(c.HeadMAC) > 0 {
		in.HeadPrefix = hex.EncodeToString(c.HeadMAC[:min(len(c.HeadMAC), headPrefixBytes)])
	}
	if c.Verified && !c.Anchored { // consistent, but nothing independent shows the tail was not cut
		in.ChainVerified, in.Reason = VerifiedUnanchored, c.Reason
		if in.Reason == "" {
			in.Reason = AnchorNoWitness
		}
	}
	if !c.Verified {
		in.ChainVerified, in.HeadSeq, in.HeadPrefix = VerifiedNo, 0, ""
		in.Reason = c.Reason
		if c.Reason == "key_unavailable" {
			in.ChainVerified = VerifiedUnknown
		}
	}
	return in
}

func buildRun(r store.Run) *RunInfo {
	return &RunInfo{RunID: fmt.Sprintf("%s/%d", r.Profile, r.Generation), Generation: r.Generation, CharterID: r.CharterID,
		State: string(r.State), Active: r.State != store.StateQuiescent, Stopping: r.State != store.StateRunning}
}

func positive(n int64) *int64 {
	if n <= 0 {
		return nil
	}
	return &n
}

func buildSpend(v store.View) *SpendInfo {
	s := &SpendInfo{NoCeiling: v.Limits.NoCeiling, RunSpendCapMicro: positive(v.RunSpendCapMicro), RateBasis: RateBasisNone, CountersAuthenticated: false, Counters: []Counter{}}
	if !v.Limits.NoCeiling {
		s.CapMicro = positive(v.Limits.BudgetMicro)
	}
	for _, t := range v.Totals {
		c := Counter{Kind: t.Kind, Total: t.Total}
		switch t.Kind {
		case "ask_spend_micro":
			s.AskMicro = t.Total
			c.Limit = positive(v.RunSpendCapMicro)
		case "grant_spend_micro":
			s.GrantMicro = t.Total
		case "ask_tasks":
			c.Limit = positive(v.RunTasksCap)
		}
		s.Counters = append(s.Counters, c)
	}
	s.UsedMicro = s.AskMicro + s.GrantMicro
	return s
}

func buildTree(m store.MemberSummary) *TreeInfo {
	t := &TreeInfo{Total: m.Total, Live: m.Live, Truncated: m.Truncated, Clusters: []Cluster{}}
	for _, c := range m.Clusters {
		t.Clusters = append(t.Clusters, Cluster{Kind: c.Kind, Total: c.Total, Live: c.Live})
	}
	return t
}

// BuildOverview maps a store view to the contract. A view whose chain does not verify carries no tree and no spend.
func BuildOverview(v store.View, now int64) Overview {
	o := Overview{SchemaVersion: ControlRoomSchema, Available: true, GeneratedAt: now, Integrity: buildIntegrity(v.Chain)}
	if v.Run.Generation > 0 { // a run row that does not verify has no identity to show
		o.Run = buildRun(v.Run)
	}
	if v.Chain.Verified { // a consistent chain, anchored or not: the badge says which
		o.Tree, o.Spend = buildTree(v.Members), buildSpend(v)
	}
	return o
}

// BuildFeed maps a store view to one page of the feed.
func BuildFeed(v store.View, now int64) FeedPage {
	p := FeedPage{SchemaVersion: ControlRoomSchema, Available: true, GeneratedAt: now, Integrity: buildIntegrity(v.Chain), Items: []FeedRow{}, NextBefore: v.FeedNext}
	for _, it := range v.Feed {
		p.Items = append(p.Items, FeedRow{Seq: it.Seq, Kind: it.Kind, Class: string(it.Class), Op: it.Op, Result: it.Result,
			AmountMicro: it.AmountMicro, Count: it.Count, AnswerClass: it.AnswerClass, Exceeded: it.Exceeded, Ref: it.Ref})
	}
	return p
}

// unknownIntegrity is the badge of a read that failed: nothing is known, so nothing is healthy.
func unknownIntegrity(code string) *IntegrityInfo {
	return &IntegrityInfo{ChainVerified: VerifiedUnknown, Reason: code}
}

func unavailableOverview(reason, code string) Overview {
	o := Overview{SchemaVersion: ControlRoomSchema, Reason: reason, ErrorCode: code}
	if reason == ReasonError {
		o.Integrity = unknownIntegrity(code)
	}
	return o
}

func unavailableFeed(reason, code string) FeedPage {
	p := FeedPage{SchemaVersion: ControlRoomSchema, Reason: reason, ErrorCode: code, Items: []FeedRow{}}
	if reason == ReasonError {
		p.Integrity = unknownIntegrity(code)
	}
	return p
}
