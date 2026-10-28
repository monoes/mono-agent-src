package extensioncli

import (
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/extension"
)

type fakeBridge struct {
	browsers []extension.ConnInfo
	listErr  error
	failOn   map[string]bool
	unbound  []string
	sets     map[string][]string
}

func (f *fakeBridge) Browsers() ([]extension.ConnInfo, error) { return f.browsers, f.listErr }
func (f *fakeBridge) SetBinding(instance string, profiles []string, label string) error {
	if label != "" {
		return errors.New("no label change is expected")
	}
	if f.failOn[instance] {
		return errors.New("browser went away")
	}
	f.unbound = append(f.unbound, instance)
	if f.sets == nil {
		f.sets = map[string][]string{}
	}
	f.sets[instance] = profiles
	return nil
}

func TestUnbindProfileUnbindsOnlyThatProfilesBrowsers(t *testing.T) {
	f := &fakeBridge{browsers: []extension.ConnInfo{
		{Instance: "a", Profile: "p1", Version: "1.5.0"}, {Instance: "b", Profile: "p2"}, {Instance: "c", Profile: "p1", Version: "1.5.0"},
		{Instance: "old", Profile: "p1", Legacy: true}, {Instance: "free"},
	}}
	n, err := unbindProfile(f, "p1")
	if err != nil || n != 2 || strings.Join(f.unbound, ",") != "a,c" {
		t.Fatalf("unbound %v (n=%d, err=%v), want a and c", f.unbound, n, err)
	}
}

// One browser failing must not leave the later ones bound.
func TestUnbindProfileGoesOnAfterAFailureAndReportsEveryOne(t *testing.T) {
	f := &fakeBridge{
		browsers: []extension.ConnInfo{{Instance: "a", Profile: "p1"}, {Instance: "b", Profile: "p1"}, {Instance: "c", Profile: "p1"}, {Instance: "d", Profile: "p1"}},
		failOn:   map[string]bool{"a": true, "c": true},
	}
	n, err := unbindProfile(f, "p1")
	if n != 2 || strings.Join(f.unbound, ",") != "b,d" {
		t.Fatalf("unbound %v (n=%d), want b and d", f.unbound, n)
	}
	if err == nil || !strings.Contains(err.Error(), "unbinding a") || !strings.Contains(err.Error(), "unbinding c") {
		t.Fatalf("err %v, want both failures named", err)
	}
}

func TestUnbindProfileReportsAnUnreachableBridge(t *testing.T) {
	if _, err := unbindProfile(&fakeBridge{listErr: errors.New("refused")}, "p1"); err == nil {
		t.Fatal("a bridge that cannot list its browsers must be reported")
	}
}

// A browser shared with other profiles keeps them: only the deleted
// profile leaves its set.
func TestUnbindProfileKeepsTheOtherProfilesOfASharedBrowser(t *testing.T) {
	f := &fakeBridge{browsers: []extension.ConnInfo{
		{Instance: "a", Version: "1.12.0", Profile: "p2", ProfileIDs: []string{"p2", "p1", "p3"}},
		{Instance: "b", Version: "1.12.0", Profile: "p1", ProfileIDs: []string{"p1"}},
	}}
	n, err := unbindProfile(f, "p1")
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if got := strings.Join(f.sets["a"], ","); got != "p2,p3" {
		t.Fatalf("a runs %q, want p2,p3", got)
	}
	if len(f.sets["b"]) != 0 {
		t.Fatalf("b runs %v, want nothing", f.sets["b"])
	}
}

// A bridge from before sets reports only the first profile of a browser
// that may run several; writing back a set computed from it would drop the
// rest, so nothing is written.
func TestUnbindProfileRefusesASetItCannotSee(t *testing.T) {
	f := &fakeBridge{browsers: []extension.ConnInfo{{Instance: "a", Version: "1.12.0", Profile: "p1"}}}
	n, err := unbindProfile(f, "p1")
	if n != 0 || err == nil || len(f.unbound) != 0 {
		t.Fatalf("n=%d err=%v unbound=%v, want a refusal and no write", n, err, f.unbound)
	}
}
