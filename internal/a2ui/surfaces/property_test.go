package surfaces

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/a2ui"
)

// The property test feeds random hil_pending maps: each result is either a
// surface a real registry accepts that shows every field whole, or an error;
// never an invalid surface or a partial one.
func TestRandomHILRecordsNeverBuildInvalidSurfaces(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	alphabets := []string{"abc", "üñ日本語", "\x00\x01\t\n", "😀é", `"\<>&/~`}
	rs := func(max int) string {
		al := []rune(alphabets[rng.Intn(len(alphabets))])
		n := rng.Intn(max + 1)
		out := make([]rune, n)
		for i := range out {
			out[i] = al[rng.Intn(len(al))]
		}
		s := string(out)
		if rng.Intn(10) == 0 {
			s += "\xff\xfe"
		}
		return s
	}
	ok, refused := 0, 0
	for i := 0; i < 300; i++ {
		it := HILItem{ID: fmt.Sprint(i), NodeName: rs(300), WorkflowName: rs(300), ReadonlyData: map[string]any{}, EditableData: map[string]any{}}
		for n := rng.Intn([]int{6, 6, 41}[rng.Intn(3)]); n > 0; n-- {
			var v any = rs([]int{10, 200, 200, 200, 6000}[rng.Intn(5)])
			switch rng.Intn(6) {
			case 0:
				v = rng.Float64() * 1e6
			case 1:
				v = map[string]any{"nested": []any{1, "x"}}
			}
			it.ReadonlyData[rs(120)] = v
		}
		for n := rng.Intn(6); n > 0; n-- {
			it.EditableData[rs(100)] = rs([]int{10, 10, 10, 500, 500, 2000, 30000}[rng.Intn(7)])
		}
		msgs, err := HIL(it)
		if err != nil {
			refused++
			if err.Error() == "" {
				t.Fatal("empty error")
			}
			continue
		}
		ok++
		s := surfaceOf(t, msgs, "hil-"+it.ID)
		if len(s.Components) > a2ui.DefaultLimits.MaxComponents || !s.DeclaresAction(btnApprove, "hil.approve") {
			t.Fatalf("iteration %d: bad surface", i)
		}
		shown := map[any]any{}
		for n := 0; ; n++ {
			k, has := s.Components[fmt.Sprintf("ro-%d-k", n)]
			if !has {
				break
			}
			shown[k["text"]] = s.Components[fmt.Sprintf("ro-%d-v", n)]["text"]
		}
		for k, v := range it.ReadonlyData {
			got, has := shown[strings.ToValidUTF8(k, "\uFFFD")]
			// keys that differ only in invalid bytes read alike on a card
			if !has || (utf8.ValidString(k) && got != strings.ToValidUTF8(displayText(v), "\uFFFD")) {
				t.Fatalf("iteration %d: field %q is not shown whole", i, k)
			}
		}
	}
	if ok < 30 || refused < 30 {
		t.Fatalf("the generator should hit both outcomes, got %d built, %d refused", ok, refused)
	}
}
