package a2ui

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// A component may have one parent. Without that rule, nine components that each
// list the next one forty times are about 40^8 visits to walk.
func TestCheckRefusesSharedChildrenWithoutWalkingThemAll(t *testing.T) {
	var comps []string
	for i := 0; i < DefaultLimits.MaxDepth; i++ {
		id, next := fmt.Sprintf("c%d", i), fmt.Sprintf("c%d", i+1)
		if i == 0 {
			id = "root"
		}
		kids := strings.TrimSuffix(strings.Repeat(`"`+next+`",`, DefaultLimits.MaxChildren), ",")
		comps = append(comps, `{"id":"`+id+`","component":"Column","children":[`+kids+`]}`)
	}
	comps = append(comps, fmt.Sprintf(`{"id":"c%d","component":"Text","text":"x"}`, DefaultLimits.MaxDepth))
	r := reg(t, FromAgent, create, `{"version":"1.0","updateComponents":{"surfaceId":"s1","components":[`+strings.Join(comps, ",")+`]}}`)
	s := r.Surface("s1")
	done := make(chan error, 1)
	go func() { done <- s.Check(DefaultLimits) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "more than once") {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Check is still walking after 5s: shared children are walked once per reference")
	}
}

func TestCheckRefusesOneComponentUnderTwoParents(t *testing.T) {
	r := reg(t, FromAgent, create, comps(`{"id":"root","component":"Column","children":["a","b"]},
	 {"id":"a","component":"Card","child":"t"},{"id":"b","component":"Card","child":"t"},{"id":"t","component":"Text","text":"x"}`))
	if err := r.Surface("s1").Check(DefaultLimits); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("err = %v", err)
	}
}
