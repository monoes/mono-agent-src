package hilsend

import (
	"encoding/json"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/monoes/mono-agent/internal/workflow"
)

// Classified is a send a Classifier found: what it does, where its parts
// live in the node's config, the app it goes through, and the params the
// node resolves per input item.
type Classified struct {
	Action            string
	Channel           string
	To, Text, Subject []string
	// ToLabel names what a To param holds when it is no address ("invoice"),
	// ToUnset the recipient when no To param is set.
	ToLabel map[string]string
	ToUnset string
	// Op and Show describe a send of keys the op doesn't declare: the op
	// ("trello card/update") and the param paths that hold the keys, which
	// the card shows as its text (undeclaredText); Exprs, those of the paths
	// given as an expression.
	Op      string
	Show    []string
	Exprs   []string
	PerItem []string
}

// Classifier classifies node types this package's table doesn't know: the
// API packages' annotated ops (internal/apipkg SendOf, installed by
// noderegistry). Without one, those node types send as before.
type Classifier interface {
	// Classify reports whether running nodeType with config sends.
	Classify(nodeType string, config map[string]interface{}) (Classified, bool)
	// SendsSome reports whether nodeType sends for some ops.
	SendsSome(nodeType string) bool
}

type classifierBox struct{ c Classifier }

var installed atomic.Value // classifierBox

// SetClassifier installs c for the process; nil removes it.
func SetClassifier(c Classifier) { installed.Store(classifierBox{c}) }

func classifierOf() Classifier {
	b, _ := installed.Load().(classifierBox)
	return b.c
}

// channelOf is the channel the card shows: a Classifier's app, else the
// node type's.
func (t target) channelOf(nodeType string) string {
	if t.channel != "" {
		return t.channel
	}
	return channelOf(nodeType)
}

// accountOf is the sending account: on a Classifier's app, the account
// param of the node as for the node types this package knows.
func (t target) accountOf(nodeType string, config map[string]interface{}) Account {
	a := accountOf(nodeType, config)
	if t.channel != "" {
		a.Platform = t.channel
	}
	return a
}

// splitPerItem makes one send per input item of a node that resolves its
// params per item (an API package's app node): each item's config has
// those params resolved against it, and performs only that item, so the
// card shows the recipient and text that item would send and the user
// judges each one. Without input items the node sends once, as configured.
func splitPerItem(t target, config map[string]interface{}, input workflow.NodeInput) []one {
	if len(input.Items) == 0 {
		return split(t, config, nil)
	}
	engine := workflow.NewExpressionEngine()
	out := make([]one, 0, len(input.Items))
	for _, it := range input.Items {
		cfg := cloneMap(config)
		held := map[string]interface{}{}
		for _, k := range t.perItem {
			if v, ok := config[k]; ok {
				held[k] = v
			}
		}
		if resolved, err := engine.ResolveConfig(held, workflow.ExpressionContext{
			JSON: it.JSON, Node: input.NodeOutputs, WorkflowID: input.WorkflowID, ExecutionID: input.ExecutionID,
		}); err == nil {
			for k, v := range resolved {
				cfg[k] = v
			}
		}
		rec, labelled := t.recipientOf(cfg)
		out = append(out, one{recipient: rec, labelled: labelled, config: cfg, input: []map[string]interface{}{it.JSON}})
	}
	return out
}

// valueAt is m's value at a send's param name: a top-level param, or for
// "a.b" option b of collection param a (an API package's annotated op,
// "updateFields.publicReply").
func valueAt(m map[string]interface{}, key string) interface{} {
	if v, ok := m[key]; ok || !strings.Contains(key, ".") {
		return v
	}
	head, rest, _ := strings.Cut(key, ".")
	sub, _ := m[head].(map[string]interface{})
	return valueAt(sub, rest)
}

// setAt sets m's value at key as valueAt reads it, copying the collection it
// writes into.
func setAt(m map[string]interface{}, key string, v interface{}) {
	head, rest, dotted := strings.Cut(key, ".")
	if _, ok := m[key]; ok || !dotted {
		m[key] = v
		return
	}
	sub, _ := m[head].(map[string]interface{})
	sub = cloneMap(sub)
	setAt(sub, rest, v)
	m[head] = sub
}

// recipientOf names who a send goes to on its card: the first of its
// recipient params that is set, labelled when the op says what it holds
// ("invoice inv9"), else the op's name for no recipient ("the ticket's
// requester"). labelled reports a name that is no person's to look up.
// An op that names its recipient when none of its params is set means
// that name, not a guess from the config's to, user or channel keys.
func (t target) recipientOf(config map[string]interface{}) (r Recipient, labelled bool) {
	keys := append([]string{}, t.to...)
	if t.toUnset == "" {
		keys = append(keys, toKeys...)
	}
	key, to := firstKey(config, keys)
	switch to = strings.TrimSpace(to); {
	case to == "" && t.toUnset != "":
		return Recipient{Name: t.toUnset, Label: true}, true
	case to != "" && t.toLabel[key] != "":
		return Recipient{Name: t.toLabel[key] + " " + to, Label: true}, true
	}
	return Recipient{Name: to, ProfileURL: profileLike(to)}, false
}

// undeclaredText is the card's text for a send of keys its op doesn't
// declare: the op, and each param that holds them with its value as this
// item would send it, so the user sees what they approve. A param given as
// an expression (exprs) says so: its keys are the run's, and on an empty
// item it resolves to "", which would read as nothing undeclared.
func undeclaredText(op string, show, exprs []string, config map[string]interface{}) string {
	var b strings.Builder
	b.WriteString(op + " with fields it doesn't declare:")
	for _, path := range show {
		if slices.Contains(exprs, path) {
			b.WriteString("\n" + path + ": (an expression)")
			continue
		}
		v, _ := json.Marshal(valueAt(config, path))
		if len(v) > 500 {
			v = append(v[:500], "…"...)
		}
		b.WriteString("\n" + path + ": " + string(v))
	}
	return b.String()
}
