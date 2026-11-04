//go:build !linux

package machine

import (
	"context"
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

func TestOtherPlatformsAreUnsupported(t *testing.T) {
	if _, err := New("/").Sample(context.Background()); !errors.Is(err, governor.ErrUnsupported) {
		t.Fatal(err)
	}
}
