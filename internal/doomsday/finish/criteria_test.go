package finish

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSplitArgvNeedsNoShellAndRefusesOperators(t *testing.T) {
	got, err := SplitArgv(`go test -run 'A B' "x \"y\"" a\ b`)
	if err != nil || !reflect.DeepEqual(got, []string{"go", "test", "-run", "A B", `x "y"`, "a b"}) {
		t.Fatalf("%q %v", got, err)
	}
	for _, bad := range []string{"", "  ", "a | b", "a; b", "echo $HOME", "ls *", "a > f", "echo `x`", "echo 'open", "a\nb"} {
		if _, err := SplitArgv(bad); !errors.Is(err, ErrCriterion) {
			t.Fatalf("%q was accepted: %v", bad, err)
		}
	}
	if _, err := SplitArgv(`echo "a|b" 'c;d'`); err != nil {
		t.Fatalf("quoted operators are plain text: %v", err)
	}
}

func TestParsePlanDefaults(t *testing.T) {
	p, err := ParsePlan("command", "true", "")
	if err != nil || !reflect.DeepEqual(p.Exits, []int{0}) || p.Compare {
		t.Fatalf("%+v %v", p, err)
	}
	if p, _ = ParsePlan("command", "x", "exit:0,2"); !reflect.DeepEqual(p.Exits, []int{0, 2}) || p.Compare {
		t.Fatalf("%+v", p)
	}
	if p, _ = ParsePlan("command", "x", "hello\n"); !p.Compare || p.Stdout != "hello" {
		t.Fatalf("%+v", p)
	}
	for _, c := range [][3]string{{"output", "x", ""}, {"output", "x", "exit:0"}, {"command", "x", "exit:300"}, {"command", "x", "exit:a"},
		{"file", "../x", "exists"}, {"file", "/etc/passwd", "exists"}, {"file", "a/../../x", "exists"}, {"file", "a", "sha256:zz"}, {"file", "a", "size<=-1"},
		{"test", "go test", ""}, {"rubric", "x", ""}, {"bogus", "x", ""}} {
		if _, err := ParsePlan(c[0], c[1], c[2]); !errors.Is(err, ErrCriterion) {
			t.Fatalf("%v accepted: %v", c, err)
		}
	}
	if p, err = ParsePlan("file", "a/./b.txt", "sha256:"+strings.Repeat("ab", 32)); err != nil || p.Path != "a/b.txt" || p.Pred != "sha256" {
		t.Fatalf("%+v %v", p, err)
	}
}
