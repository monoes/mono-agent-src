package control

import (
	"fmt"
	"testing"
)

func TestConvertTypeN8nSpellings(t *testing.T) {
	cases := []struct {
		val  interface{}
		typ  string
		want interface{}
	}{
		{"true", "boolean", true},
		{"no", "Boolean", false},
		{"42", "integer", float64(42)},
		{"1.5", "float", 1.5},
		{`[1,2]`, "array", []interface{}{float64(1), float64(2)}},
		{`{"a":1}`, "object", map[string]interface{}{"a": float64(1)}},
	}
	for _, c := range cases {
		got, err := convertType(c.val, c.typ)
		if err != nil {
			t.Fatalf("convertType(%v, %s): %v", c.val, c.typ, err)
		}
		if fmtVal(got) != fmtVal(c.want) {
			t.Errorf("convertType(%v, %s) = %#v, want %#v", c.val, c.typ, got, c.want)
		}
	}
}

func fmtVal(v interface{}) string { return fmt.Sprintf("%T %v", v, v) }
