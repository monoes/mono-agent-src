package n8nimport

import (
	"strconv"
	"strings"
)

// globalNames are identifiers that only mean something with a member or a
// call after them.
var globalNames = map[string]bool{
	"$": true, "$items": true, "$ifEmpty": true, "$node": true, "$env": true, "$vars": true,
	"$execution": true, "$workflow": true, "String": true, "Number": true, "parseInt": true,
	"parseFloat": true, "Boolean": true, "encodeURIComponent": true, "encodeURI": true,
	"JSON": true, "Math": true, "DateTime": true, "Object": true, "Date": true, "Array": true,
	"$fromAI": true, "$fromAi": true, "$fromai": true,
}

func (e *emitter) ident(name string) (val, error) {
	switch name {
	case "$json", "$data":
		return val{p: "$json"}, nil
	case "$input":
		return val{obj: objInput, name: name}, nil
	case "$now", "$today":
		return val{obj: objDateTime, name: strings.TrimPrefix(name, "$")}, nil
	case "$runIndex":
		return val{p: "$runIndex"}, nil
	}
	if globalNames[name] {
		return val{obj: objGlobal, name: name}, nil
	}
	return val{}, unsupported("%s", name)
}

// memberKey returns a literal member key: the name of a.b, or a string or
// number literal of a[…]. ok is false for a computed, dynamic key.
func memberKey(n *jsNode) (key string, isNum bool, ok bool) {
	if !n.computed {
		return n.name, false, true
	}
	switch k := n.args[1]; k.kind {
	case jsStr:
		return k.str, false, true
	case jsNum:
		return formatNum(k.num), true, true
	}
	return "", false, false
}

func (e *emitter) member(n *jsNode) (val, error) {
	recv, err := e.emit(n.args[0])
	if err != nil {
		return val{}, err
	}
	key, isNum, literal := memberKey(n)
	switch recv.obj {
	case objNone:
		return e.valueMember(recv, n, key, isNum, literal)
	case objGlobal:
		return e.globalMember(recv.name, key, literal)
	case objNodeRef:
		if key == "item" {
			return val{obj: objNodeItem, name: recv.name}, nil
		}
	case objNodeItem:
		if key == "json" {
			if recv.last {
				e.note("$('%s').last() reads the first item of %q (mono-agent exposes the first item of other nodes)", recv.name, recv.name)
			}
			return val{p: "(index $node " + strconv.Quote(recv.name) + ").json"}, nil
		}
	case objItemsList:
		if isNum && key == "0" {
			return val{obj: objNodeItem, name: recv.name}, nil
		}
	case objInput:
		if key == "item" {
			return val{obj: objInputItem}, nil
		}
	case objInputItem:
		if key == "json" {
			if recv.last {
				e.note("$input.last() reads the current (first) item in mono-agent")
			}
			return val{p: "$json"}, nil
		}
	case objDateTime, objJSDate:
		return e.dateMember(recv, key)
	}
	return val{}, unsupported("%s.%s", describeObj(recv), key)
}

func describeObj(v val) string {
	switch v.obj {
	case objNodeRef:
		return "$('" + v.name + "')"
	case objNodeItem:
		return "$('" + v.name + "').item"
	case objItemsList:
		return "$items('" + v.name + "')"
	case objInput:
		return "$input"
	case objInputItem:
		return "$input.item"
	case objDateTime:
		return "$" + v.name
	case objJSDate:
		return "new Date()"
	case objNull:
		return "null"
	}
	if v.name != "" {
		return v.name
	}
	return v.p
}

func (e *emitter) valueMember(recv val, n *jsNode, key string, isNum, literal bool) (val, error) {
	if !n.computed && key == "length" {
		return call(tNum, "len", recv.p), nil
	}
	chainable := strings.HasPrefix(recv.p, "$") || strings.HasPrefix(recv.p, "(")
	switch {
	case literal && isNum:
		if strings.HasPrefix(key, "-") || strings.Contains(key, ".") {
			return val{}, unsupported("index %s", key)
		}
		return val{p: "(index " + recv.p + " " + key + ")"}, nil
	case literal && goIdent.MatchString(key) && chainable:
		return val{p: recv.p + "." + key}, nil
	case literal:
		return val{p: "(index " + recv.p + " " + strconv.Quote(key) + ")"}, nil
	}
	k, err := e.value(n.args[1])
	if err != nil {
		return val{}, err
	}
	return val{p: "(index " + recv.p + " " + k.p + ")"}, nil
}

func (e *emitter) globalMember(ns, key string, literal bool) (val, error) {
	if !literal {
		return val{}, unsupported("a computed member of %s", ns)
	}
	switch ns {
	case "$node":
		return val{obj: objNodeItem, name: key}, nil
	case "$env", "$vars":
		if !goIdent.MatchString(key) {
			return val{}, unsupported("%s[%q]", ns, key)
		}
		if ns == "$vars" {
			e.note("n8n variable $vars.%s has no mono-agent equivalent; it now reads the environment variable %s", key, key)
		}
		e.note("$env templates resolve only when MONOAGENT_ALLOW_ENV_TEMPLATES=1")
		return val{p: "$env." + key, t: tStr}, nil
	case "$execution":
		if key == "id" {
			return val{p: "$execution.id", t: tStr}, nil
		}
	case "$workflow":
		switch key {
		case "id":
			return val{p: "$workflow.id", t: tStr}, nil
		case "name":
			return strVal(e.workflowName), nil
		}
	}
	return val{}, unsupported("%s.%s", ns, key)
}

func literalString(n *jsNode) (string, bool) {
	if n != nil && n.kind == jsStr {
		return n.str, true
	}
	return "", false
}
