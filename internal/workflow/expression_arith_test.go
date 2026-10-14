package workflow

import "testing"

// Code nodes emit whole numbers as int64 and $runIndex is an int; the
// arithmetic helpers take them like float64.
func TestExpressionArithmeticAcceptsIntegers(t *testing.T) {
	e := NewExpressionEngine()
	ctx := ExpressionContext{JSON: map[string]interface{}{"n": int64(3), "f": 1.5, "s": "2"}, RunIndex: 2}
	for _, tc := range []struct{ tmpl, want string }{
		{`{{ mul $json.n 2 }}`, "6"},
		{`{{ add $json.n $json.f }}`, "4.5"},
		{`{{ add $runIndex 1 }}`, "3"},
		{`{{ sub $json.s 1 }}`, "1"},
		{`{{ div $json.n 2 }}`, "1.5"},
	} {
		got, err := e.EvaluateString(tc.tmpl, ctx)
		if err != nil || got != tc.want {
			t.Errorf("%s = %q, %v; want %q", tc.tmpl, got, err, tc.want)
		}
	}
	if _, err := e.EvaluateString(`{{ div $json.n 0 }}`, ctx); err == nil {
		t.Error("div by zero: want an error")
	}
}
