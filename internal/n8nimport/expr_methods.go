package n8nimport

import (
	"strconv"
	"strings"
)

func (e *emitter) callExpr(n *jsNode) (val, error) {
	callee, args := n.args[0], n.args[1:]
	if callee.kind == jsIdent {
		return e.callFunc(callee.name, args)
	}
	if callee.kind != jsMember || callee.computed {
		return val{}, unsupported("calling a computed function")
	}
	method := callee.name
	// "a".split(x).join(y) is a replace, which mono can express.
	if method == "join" && callee.args[0].kind == jsCall {
		inner := callee.args[0].args[0]
		if inner.kind == jsMember && inner.name == "split" && len(callee.args[0].args) == 2 && len(args) == 1 {
			return e.replaceCall(inner.args[0], callee.args[0].args[1], args[0], true)
		}
	}
	recv, err := e.emit(callee.args[0])
	if err != nil {
		return val{}, err
	}
	switch recv.obj {
	case objNone:
		return e.valueMethod(recv, method, callee.args[0], args)
	case objGlobal:
		return e.globalMethod(recv.name, method, args)
	case objNodeRef:
		switch method {
		case "first", "itemMatching":
			if method == "itemMatching" {
				e.note("$('%s').itemMatching() reads the first item of %q", recv.name, recv.name)
			}
			return val{obj: objNodeItem, name: recv.name}, nil
		case "last":
			return val{obj: objNodeItem, name: recv.name, last: true}, nil
		}
	case objInput:
		switch method {
		case "first":
			return val{obj: objInputItem}, nil
		case "last":
			return val{obj: objInputItem, last: true}, nil
		}
	case objDateTime, objJSDate:
		return e.dateMethod(recv, method, args)
	}
	return val{}, unsupported("%s.%s()", describeObj(recv), method)
}

func (e *emitter) callFunc(name string, args []*jsNode) (val, error) {
	switch name {
	case "$":
		if s, ok := literalString(first(args)); ok && len(args) == 1 {
			return val{obj: objNodeRef, name: s}, nil
		}
		return val{}, unsupported("$() with a computed node name")
	case "$items":
		s, ok := literalString(first(args))
		if !ok {
			return val{}, unsupported("$items() with a computed node name")
		}
		for _, a := range args[1:] {
			if a.kind != jsNum || a.num != 0 {
				return val{}, unsupported("$items() with an output or run index")
			}
		}
		return val{obj: objItemsList, name: s}, nil
	case "$fromAI", "$fromAi", "$fromai":
		return e.fromAI(args)
	}
	if len(args) == 0 {
		return val{}, unsupported("%s()", name)
	}
	x, err := e.value(args[0])
	if err != nil {
		return val{}, err
	}
	switch name {
	case "$ifEmpty":
		if len(args) != 2 {
			break
		}
		d, err := e.value(args[1])
		if err != nil {
			return val{}, err
		}
		return call(tAny, "default", d.p, x.p), nil
	case "String":
		return val{p: asStr(x), t: tStr}, nil
	case "Number", "parseFloat":
		return val{p: asNum(x), t: tNum}, nil
	case "parseInt":
		if len(args) == 1 || (args[1].kind == jsNum && args[1].num == 10) {
			return call(tNum, "toInt", x.p), nil
		}
	case "Boolean":
		return call(tBool, "not", "(not "+x.p+")"), nil
	case "encodeURIComponent", "encodeURI":
		e.note("%s() became urlquery, which encodes spaces as '+'", name)
		return call(tStr, "urlquery", x.p), nil
	}
	return val{}, unsupported("%s()", name)
}

// fromAI translates $fromAI(key, description, type, default), the value an
// agent fills in when it calls the node as a tool: the tool call's
// arguments are the item, so it reads $json.key.
func (e *emitter) fromAI(args []*jsNode) (val, error) {
	key, ok := literalString(first(args))
	if !ok || key == "" {
		return val{}, unsupported("$fromAI() without a literal key")
	}
	p := "$json." + key
	if !goIdent.MatchString(key) {
		p = "(index $json " + strconv.Quote(key) + ")"
	}
	e.note("$fromAI(%q) reads the tool call's argument %q from the item", key, key)
	if len(args) >= 4 {
		d, err := e.value(args[3])
		if err != nil {
			return val{}, err
		}
		return call(tAny, "default", d.p, p), nil
	}
	return val{p: p}, nil
}

func first(args []*jsNode) *jsNode {
	if len(args) == 0 {
		return nil
	}
	return args[0]
}

func (e *emitter) globalMethod(ns, method string, args []*jsNode) (val, error) {
	switch ns + "." + method {
	case "JSON.stringify":
		if len(args) == 0 {
			break
		}
		x, err := e.value(args[0])
		if err != nil {
			return val{}, err
		}
		if len(args) > 1 {
			e.note("JSON.stringify indentation is not kept")
		}
		return call(tStr, "json", x.p), nil
	case "JSON.parse":
		if len(args) != 1 {
			break
		}
		x, err := e.value(args[0])
		if err != nil {
			return val{}, err
		}
		return call(tAny, "jsonParse", asStr(x)), nil
	case "DateTime.now", "DateTime.local", "DateTime.utc":
		if len(args) == 0 {
			return val{obj: objDateTime, name: "now"}, nil
		}
	}
	return val{}, unsupported("%s.%s()", ns, method)
}

// valueMethod handles string (and a few n8n extension) methods on a plain
// value.
func (e *emitter) valueMethod(recv val, method string, recvNode *jsNode, args []*jsNode) (val, error) {
	if (method == "replace" || method == "replaceAll") && len(args) == 2 {
		return e.replaceCall(recvNode, args[0], args[1], method == "replaceAll")
	}
	argv, err := e.emitAll(args)
	if err != nil {
		return val{}, err
	}
	x := recv.p
	switch method {
	case "toUpperCase", "toLocaleUpperCase":
		return call(tStr, "upper", x), nil
	case "toLowerCase", "toLocaleLowerCase":
		return call(tStr, "lower", x), nil
	case "trim":
		return call(tStr, "trim", x), nil
	case "trimStart", "trimLeft":
		return call(tStr, "trimLeft", `" \t\r\n"`, x), nil
	case "trimEnd", "trimRight":
		return call(tStr, "trimRight", `" \t\r\n"`, x), nil
	case "toString", "toJSON":
		if len(args) == 0 {
			return val{p: asStr(recv), t: tStr}, nil
		}
	case "includes":
		if len(args) == 1 {
			e.note("includes() was translated for text values (a list value fails at run time)")
			s := asStr(recv)
			return call(tBool, "ne", "(replace "+asStr(argv[0])+` "" `+s+")", s), nil
		}
	case "startsWith", "endsWith":
		if len(args) == 1 {
			return prefixCheck(asStr(recv), asStr(argv[0]), method == "startsWith"), nil
		}
	case "substring", "slice", "substr":
		return e.sliceCall(recv, method, args)
	case "toFixed":
		d := 0.0
		if len(args) == 1 {
			if args[0].kind != jsNum {
				break
			}
			d = args[0].num
		}
		return call(tStr, "printf", strconv.Quote("%."+formatNum(d)+"f"), asNum(recv)), nil
	case "concat":
		return e.concat(append([]*jsNode{recvNode}, args...))
	case "split":
		if len(args) == 1 {
			return call(tAny, "split", asStr(argv[0]), asStr(recv)), nil
		}
	case "isEmpty":
		return call(tBool, "not", x), nil
	case "isNotEmpty":
		return call(tBool, "not", "(not "+x+")"), nil
	case "toNumber", "toFloat":
		return val{p: asNum(recv), t: tNum}, nil
	case "toInt":
		return call(tNum, "toInt", x), nil
	case "toBoolean":
		return call(tBool, "toBool", x), nil
	case "toJsonString":
		return call(tStr, "json", x), nil
	case "urlEncode":
		e.note("urlEncode() became urlquery, which encodes spaces as '+'")
		return call(tStr, "urlquery", x), nil
	case "first":
		if len(args) == 0 {
			return val{p: "(index " + x + " 0)"}, nil
		}
	}
	return val{}, unsupported(".%s()", method)
}

// replaceCall emits recv.replace(from, to). Only literal, non-pattern
// searches translate; JavaScript's replace() of a string changes the first
// match only, mono's replace changes all of them.
func (e *emitter) replaceCall(recvNode, fromNode, toNode *jsNode, all bool) (val, error) {
	recv, err := e.value(recvNode)
	if err != nil {
		return val{}, err
	}
	var from val
	if fromNode.kind == jsRegex {
		body, flags, _ := strings.Cut(fromNode.str, "/")
		lit, ok := regexLiteral(body)
		if !ok {
			return val{}, unsupported("a regular expression in replace()")
		}
		from = strVal(lit)
		all = all || strings.Contains(flags, "g")
		if strings.Contains(flags, "i") {
			return val{}, unsupported("a case-insensitive regular expression in replace()")
		}
	} else if from, err = e.value(fromNode); err != nil {
		return val{}, err
	}
	to, err := e.value(toNode)
	if err != nil {
		return val{}, err
	}
	if to.strLit && strings.Contains(to.str, "$") {
		return val{}, unsupported("replacement patterns ($&, $1) in replace()")
	}
	if !all {
		e.note("replace() now replaces every occurrence, not just the first")
	}
	return call(tStr, "replace", asStr(from), asStr(to), asStr(recv)), nil
}

// regexLiteral returns the literal text a regex body matches when it has no
// metacharacters other than escaped ones.
func regexLiteral(body string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c == '\\' && i+1 < len(body) {
			n := body[i+1]
			if strings.IndexByte(`\/.*+?()[]{}|^$-`, n) < 0 {
				if n == 'n' {
					b.WriteByte('\n')
					i++
					continue
				}
				if n == 't' {
					b.WriteByte('\t')
					i++
					continue
				}
				return "", false
			}
			b.WriteByte(n)
			i++
			continue
		}
		if strings.IndexByte(`.*+?()[]{}|^$`, c) >= 0 {
			return "", false
		}
		b.WriteByte(c)
	}
	return b.String(), b.Len() > 0
}

// prefixCheck emits startsWith / endsWith with len and slice.
func prefixCheck(s, p string, start bool) val {
	long := "(ge (len " + s + ") (len " + p + "))"
	if start {
		return call(tBool, "and", long, "(eq (slice "+s+" 0 (len "+p+")) "+p+")")
	}
	return call(tBool, "and", long, "(eq (slice "+s+" (toInt (sub (toFloat (len "+s+")) (toFloat (len "+p+"))))) "+p+")")
}

func (e *emitter) sliceCall(recv val, method string, args []*jsNode) (val, error) {
	if len(args) == 0 || len(args) > 2 {
		return val{}, unsupported(".%s() with %d arguments", method, len(args))
	}
	idx := make([]int, len(args))
	for i, a := range args {
		if a.kind != jsNum || a.num < 0 || a.num != float64(int(a.num)) {
			return val{}, unsupported(".%s() with a negative or computed position", method)
		}
		idx[i] = int(a.num)
	}
	s := asStr(recv)
	if len(idx) == 1 {
		return call(tStr, "slice", s, strconv.Itoa(idx[0])), nil
	}
	end := idx[1]
	if method == "substr" {
		end = idx[0] + idx[1]
	}
	if end < idx[0] {
		return val{}, unsupported(".%s() with an end before its start", method)
	}
	e.note(".%s() fails at run time when the text is shorter than %d characters", method, end)
	return call(tStr, "slice", s, strconv.Itoa(idx[0]), strconv.Itoa(end)), nil
}
