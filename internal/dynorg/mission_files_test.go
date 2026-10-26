package dynorg

import (
	"slices"
	"testing"
)

func TestTaskFilesParsesBlockAndInlineForms(t *testing.T) {
	notes := "Do the thing.\n\nFiles:\n- internal/a/a.go\n* `./b.go`, c/\n\nNot: this.go\n"
	if got, want := TaskFiles(notes), []string{"internal/a/a.go", "b.go", "c/"}; !slices.Equal(got, want) {
		t.Fatalf("block: %v want %v", got, want)
	}
	if got := TaskFiles("files: x.go, y/**"); !slices.Equal(got, []string{"x.go", "y/"}) {
		t.Fatalf("inline: %v", got)
	}
	if got := TaskFiles("no block here"); got != nil {
		t.Fatalf("none: %v", got)
	}
}

func TestFilesOverlap(t *testing.T) {
	for _, c := range []struct {
		a, b []string
		want bool
	}{
		{[]string{"a.go"}, []string{"a.go"}, true},
		{[]string{"a.go"}, []string{"b.go"}, false},
		{[]string{"pkg/"}, []string{"pkg/x.go"}, true},
		{[]string{"pkg/x.go"}, []string{"pkg/"}, true},
		{[]string{"pkg/"}, []string{"pkgs/x.go"}, false},
		{nil, []string{"a.go"}, true},
		{[]string{"./"}, []string{"a.go"}, true},
	} {
		if got := filesOverlap(c.a, c.b); got != c.want {
			t.Errorf("%v vs %v = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestTaskFilesReadsThePlannersMetaLine(t *testing.T) {
	notes := notesWithMeta("do the thing", TaskMeta{Files: []string{"internal/a/a.go", "internal/b/"}, Size: "s"})
	got := TaskFiles(notes)
	if len(got) != 2 || got[0] != "internal/a/a.go" {
		t.Fatalf("TaskFiles = %v, want the meta line's two paths", got)
	}
}
