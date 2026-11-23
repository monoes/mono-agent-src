package datatable

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Conditions, as n8n's Data table node names them.
const (
	OpEq         = "eq"
	OpNeq        = "neq"
	OpGt         = "gt"
	OpGte        = "gte"
	OpLt         = "lt"
	OpLte        = "lte"
	OpLike       = "like"  // case-sensitive; % matches anything
	OpIlike      = "ilike" // like, ignoring case
	OpIsEmpty    = "isEmpty"
	OpIsNotEmpty = "isNotEmpty"
	OpIsTrue     = "isTrue"
	OpIsFalse    = "isFalse"
)

// Condition compares one column with a value.
type Condition struct {
	Column string      `json:"column"`
	Op     string      `json:"condition"`
	Value  interface{} `json:"value,omitempty"`
}

// Filter selects rows: all its conditions (All) or any of them. A filter
// without conditions selects every row.
type Filter struct {
	All        bool
	Conditions []Condition
}

// compile checks the filter against t and returns its row matcher.
func (f Filter) compile(t Table) (func(row) bool, error) {
	tests := make([]func(row) bool, 0, len(f.Conditions))
	for _, c := range f.Conditions {
		test, err := c.compile(t)
		if err != nil {
			return nil, err
		}
		tests = append(tests, test)
	}
	return func(r row) bool {
		if len(tests) == 0 {
			return true
		}
		for _, test := range tests {
			if test(r) != f.All {
				return !f.All
			}
		}
		return f.All
	}, nil
}

// textEquality returns an equality on a text column that every row f selects must satisfy: the only condition, or one
// of an all-conditions filter.
func (f Filter) textEquality(t Table) (col, val string, ok bool) {
	if !f.All && len(f.Conditions) != 1 {
		return "", "", false
	}
	for _, c := range f.Conditions {
		if c.Op != "" && c.Op != OpEq {
			continue
		}
		typ, known := t.ColumnType(c.Column)
		v, isText := c.Value.(string)
		if !known || typ != TypeString || !isText || strings.ContainsAny(c.Column, `"\'`) {
			continue
		}
		return c.Column, v, true
	}
	return "", "", false
}

func (c Condition) compile(t Table) (func(row) bool, error) {
	typ, ok := t.ColumnType(c.Column)
	if !ok {
		return nil, fmt.Errorf("column %q does not exist in data table %q", c.Column, t.Name)
	}
	op := c.Op
	if op == "" {
		op = OpEq
	}
	col := c.Column
	switch op {
	case OpIsEmpty, OpIsNotEmpty:
		want := op == OpIsEmpty
		return func(r row) bool {
			v := r.value(col)
			return (v == nil || v == "") == want
		}, nil
	case OpIsTrue, OpIsFalse:
		want := op == OpIsTrue
		return func(r row) bool { b, ok := r.value(col).(bool); return ok && b == want }, nil
	case OpLike, OpIlike:
		pattern, ok := c.Value.(string)
		if !ok {
			if c.Value == nil {
				return nil, fmt.Errorf("column %q: %s needs a value", col, op)
			}
			pattern = fmt.Sprint(c.Value)
		}
		re := likePattern(pattern, op == OpIlike)
		return func(r row) bool {
			v := r.value(col)
			return v != nil && re.MatchString(cellText(v))
		}, nil
	}
	var want interface{}
	if c.Value != nil {
		var err error
		if want, err = coerce(typ, c.Value); err != nil {
			return nil, fmt.Errorf("column %q: %v", col, err)
		}
	}
	switch op {
	case OpEq, OpNeq:
		eq := op == OpEq
		return func(r row) bool {
			v := r.value(col)
			if want == nil || v == nil {
				return (v == nil && want == nil) == eq
			}
			return (compareValues(v, want, typ) == 0) == eq
		}, nil
	case OpGt, OpGte, OpLt, OpLte:
		if want == nil {
			return nil, fmt.Errorf("column %q: %s needs a value", col, op)
		}
		return func(r row) bool {
			v := r.value(col)
			if v == nil {
				return false
			}
			cmp := compareValues(v, want, typ)
			switch op {
			case OpGt:
				return cmp > 0
			case OpGte:
				return cmp >= 0
			case OpLt:
				return cmp < 0
			}
			return cmp <= 0
		}, nil
	}
	return nil, fmt.Errorf("column %q: unknown condition %q (use eq, neq, gt, gte, lt, lte, like, ilike, isEmpty, isNotEmpty, isTrue or isFalse)", col, op)
}

// likePattern is SQL LIKE with % as the wildcard; a pattern without one
// matches anywhere in the text, as n8n does.
func likePattern(pattern string, fold bool) *regexp.Regexp {
	if !strings.Contains(pattern, "%") {
		pattern = "%" + pattern + "%"
	}
	parts := strings.Split(pattern, "%")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	expr := "^" + strings.Join(parts, ".*") + "$"
	if fold {
		expr = "(?i)" + expr
	}
	return regexp.MustCompile("(?s)" + expr)
}

func cellText(v interface{}) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

// compareValues orders two non-nil values of a column type; nil sorts
// first.
func compareValues(a, b interface{}, typ string) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	}
	switch typ {
	case TypeNumber:
		x, _ := coerce(TypeNumber, a)
		y, _ := coerce(TypeNumber, b)
		fx, okx := x.(float64)
		fy, oky := y.(float64)
		if okx && oky {
			switch {
			case fx < fy:
				return -1
			case fx > fy:
				return 1
			}
			return 0
		}
	case TypeBoolean:
		x, okx := a.(bool)
		y, oky := b.(bool)
		if okx && oky {
			switch {
			case x == y:
				return 0
			case !x:
				return -1
			}
			return 1
		}
	case TypeDate:
		x, errx := time.Parse(time.RFC3339Nano, cellText(a))
		y, erry := time.Parse(time.RFC3339Nano, cellText(b))
		if errx == nil && erry == nil {
			return x.Compare(y)
		}
	}
	return strings.Compare(cellText(a), cellText(b))
}
