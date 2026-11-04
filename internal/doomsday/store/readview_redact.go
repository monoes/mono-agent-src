package store

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// FeedClass is the closed set of classes a feed row can have. The control room shows the class, amounts and refs of a
// row, never its text: a row whose kind or payload is not understood is class "other" with kind "(redacted)".
type FeedClass string

const (
	ClassRun       FeedClass = "run"
	ClassApproval  FeedClass = "approval"
	ClassPlan      FeedClass = "plan"
	ClassAskAnswer FeedClass = "ask_answer"
	ClassGrant     FeedClass = "grant"
	ClassSpend     FeedClass = "spend"
	ClassPark      FeedClass = "park"
	ClassFinish    FeedClass = "finish"
	ClassRefusal   FeedClass = "refusal"
	ClassOther     FeedClass = "other"
)

// RedactedKind is the kind shown for a row that is not on the allowlist.
const RedactedKind = "(redacted)"

// maxPayloadBytes bounds what is parsed per row; a larger payload is shown as redacted.
const maxPayloadBytes = 64 << 10

// FeedItem is one audit row after redaction. Every string in it is either from a closed set or matches refPattern, so
// no operator text, answer text, vault name or secret value can reach it.
type FeedItem struct {
	Seq         int64
	Kind        string
	Class       FeedClass
	Op          string
	Result      string
	AmountMicro *int64
	Count       *int64
	AnswerClass string
	Exceeded    []string
	Ref         string
}

var refPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,63}$`)

func safeRef(s string) string {
	if refPattern.MatchString(s) {
		return s
	}
	return ""
}

var (
	typedOps       = map[string]FeedClass{"plan.approve": ClassPlan, "ask.answer": ClassAskAnswer, "spend.grant": ClassGrant, "finish.begin": ClassFinish}
	opResults      = map[string]bool{"approved": true, "parked": true, "refused": true, "denied": true}
	counterKinds   = map[string]bool{"ask_spend_micro": true, "ask_tasks": true, "grant_spend_micro": true, "sends": true, "local_count": true, "local_bytes": true, "vault_attempts": true, "approvals": true}
	limitNames     = map[string]bool{"ask_spend_micro": true, "ask_tasks": true, "ask_access": true, "ask_depth": true, "run_spend_micro": true, "run_tasks": true, "never_liftable": true, "answer_class": true, "grant_target": true, "grant_max_per_grant_micro": true, "grant_run_ceiling_micro": true}
	runStates      = map[string]bool{"running": true, "stop-requested": true, "authority-revoked": true, "cleanup-pending": true, "quiescent": true}
	finishKindExpr = regexp.MustCompile(`^finish\.[a-z]{1,16}$`)
	classPattern   = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
)

func redacted(seq int64) FeedItem {
	return FeedItem{Seq: seq, Kind: RedactedKind, Class: ClassOther}
}

// redactRow turns one verified audit row into a FeedItem. Default deny: only the kinds below are shown, and only the
// payload fields named here are read.
func redactRow(seq int64, kind, payload string) FeedItem {
	if len(payload) > maxPayloadBytes {
		return redacted(seq)
	}
	switch {
	case kind == "run.enter" || kind == "start":
		return FeedItem{Seq: seq, Kind: kind, Class: ClassRun}
	case kind == "run.advance":
		it := FeedItem{Seq: seq, Kind: kind, Class: ClassRun}
		if tok, _, _ := strings.Cut(payload, " "); tok != "" {
			if from, to, ok := strings.Cut(tok, ">"); ok && runStates[from] && runStates[to] {
				it.Result = to
			}
		}
		return it
	case kind == "counter.consume":
		return redactConsume(seq, payload)
	case kind == "approval.issue":
		var p struct {
			ID string `json:"id"`
			Op string `json:"op"`
		}
		it := FeedItem{Seq: seq, Kind: kind, Class: ClassApproval}
		if json.Unmarshal([]byte(payload), &p) == nil {
			if _, ok := typedOps[p.Op]; ok {
				it.Op = p.Op
			}
			it.Ref = safeRef(p.ID)
		}
		return it
	case kind == "approval.rescind" || kind == "approval.consume":
		return FeedItem{Seq: seq, Kind: kind, Class: ClassApproval}
	case kind == "denied-attempt":
		return FeedItem{Seq: seq, Kind: kind, Class: ClassRefusal}
	case finishKindExpr.MatchString(kind):
		return FeedItem{Seq: seq, Kind: kind, Class: ClassFinish}
	case kind == "ops.person-item":
		return redactPersonItem(seq, payload)
	case strings.HasPrefix(kind, "ops."):
		return redactOps(seq, kind, payload)
	}
	return redacted(seq)
}

func redactConsume(seq int64, payload string) FeedItem {
	it := FeedItem{Seq: seq, Kind: "counter.consume", Class: ClassSpend}
	f := strings.Split(payload, " ")
	if len(f) != 3 || !counterKinds[f[0]] {
		return redacted(seq)
	}
	n, err := strconv.ParseInt(f[2], 10, 64)
	if err != nil || n < 0 {
		return redacted(seq)
	}
	it.Ref = safeRef(f[1])
	if strings.HasSuffix(f[0], "_micro") {
		it.AmountMicro = &n
	} else {
		it.Count = &n
	}
	it.Op = f[0]
	return it
}

func closedLimits(in []string) []string {
	var out []string
	for _, l := range in {
		if limitNames[l] && len(out) < len(limitNames) {
			out = append(out, l)
		}
	}
	return out
}

func redactPersonItem(seq int64, payload string) FeedItem {
	var p struct {
		Op       string   `json:"op"`
		Exceeded []string `json:"exceeded"`
	}
	it := FeedItem{Seq: seq, Kind: "ops.person-item", Class: ClassPark}
	if json.Unmarshal([]byte(payload), &p) == nil {
		if _, ok := typedOps[p.Op]; ok {
			it.Op = p.Op
		}
		it.Exceeded = closedLimits(p.Exceeded)
	}
	return it
}

// redactOps reads the decision rows of the typed operations (kind "ops.<op>.<result>"). The payload is the ops
// package's AuditRow (Go field names; encoding/json matches them case-insensitively).
func redactOps(seq int64, kind, payload string) FeedItem {
	rest := strings.TrimPrefix(kind, "ops.")
	i := strings.LastIndex(rest, ".")
	if i < 0 {
		return redacted(seq)
	}
	op, result := rest[:i], rest[i+1:]
	class, ok := typedOps[op]
	if !ok || !opResults[result] {
		return redacted(seq)
	}
	it := FeedItem{Seq: seq, Kind: kind, Class: class, Op: op, Result: result}
	var p struct {
		ApprovalID string   `json:"approvalid"`
		Exceeded   []string `json:"exceeded"`
		Actual     struct {
			Class      string `json:"class"`
			Tasks      int64  `json:"tasks"`
			SpendMicro int64  `json:"spendmicro"`
		} `json:"actual"`
		ActualGrant struct {
			AmountMicro int64 `json:"amountmicro"`
		} `json:"actualgrant"`
	}
	if json.Unmarshal([]byte(payload), &p) != nil {
		return it
	}
	it.Ref = safeRef(p.ApprovalID)
	it.Exceeded = closedLimits(p.Exceeded)
	if classPattern.MatchString(p.Actual.Class) {
		it.AnswerClass = p.Actual.Class
	}
	amount, count := p.Actual.SpendMicro, p.Actual.Tasks
	if op == "spend.grant" {
		amount = p.ActualGrant.AmountMicro
	}
	if amount > 0 {
		it.AmountMicro = &amount
	}
	if count > 0 {
		it.Count = &count
	}
	return it
}
