package automation

import (
	"context"
	"strings"
	"testing"
	"time"
)

// rerecord opens --url in the user's logged-in browser: only http(s), and a positive timeout,
// before anything is opened.
func TestRerecordRefusesBadURLAndTimeout(t *testing.T) {
	t.Parallel()
	dir := rerecordFixturePackage(t)
	for _, tc := range []struct {
		name, url string
		timeout   time.Duration
		want      string
	}{
		{"file", "file:///etc/passwd", time.Minute, "unsupported URL scheme"},
		{"javascript", "javascript:alert(1)", time.Minute, "unsupported URL scheme"},
		{"zero timeout", "https://acme.test/", 0, "--timeout must be positive"},
		{"negative timeout", "https://acme.test/", -time.Second, "--timeout must be positive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opened := false
			open := func(context.Context, string, bool) (elementPicker, error) {
				opened = true
				return &fakePicker{}, nil
			}
			_, err := rerecordSelector(context.Background(), open, dirGetter{dir}, &fakeReplacer{}, "acme", "save.button", tc.url, tc.timeout, false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if opened {
				t.Fatal("the picker was opened")
			}
		})
	}
}
