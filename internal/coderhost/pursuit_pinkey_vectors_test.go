package coderhost

import "testing"

var pinKeyVectors = []struct{ in, want string }{
	// BEGIN SHARED PIN VECTORS (identical in internal/missionpursuit/verifier/pin_test.go and internal/coderhost/pursuit_pinkey_vectors_test.go)
	{"runtime/model@1", "runtime/model"}, {"Runtime/Model", "runtime/model"}, {" runtime/model ", "runtime/model"},
	{"runtime/model", "runtime/model"}, {"", ""}, {"  ", ""}, {"x/y@a@b", "x/y"}, {" Claude/Opus-4 @2026", "claude/opus-4"},
	{"a / b", "a / b"}, {"@v", ""},
	// END SHARED PIN VECTORS
}

func TestPinKeyMatchesTheSharedVectors(t *testing.T) {
	for _, v := range pinKeyVectors {
		if got := pinKey(v.in); got != v.want {
			t.Errorf("pinKey(%q) = %q, want %q", v.in, got, v.want)
		}
	}
}
