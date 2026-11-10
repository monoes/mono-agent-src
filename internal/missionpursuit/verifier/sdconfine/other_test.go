//go:build !linux

package sdconfine

import (
	"context"
	"errors"
	"testing"
)

// macOS and Windows keep 'no-confinement': New returns no confiner and a typed refusal that names the platform.
func TestWhereThereIsNoSystemdNewRefusesAndNamesThePlatform(t *testing.T) {
	c, err := New(context.Background(), Config{ScratchBase: t.TempDir()})
	var u *Unavailable
	if c != nil || !errors.As(err, &u) || u.Missing != MissingPlatform {
		t.Fatalf("%v %v", c, err)
	}
}
