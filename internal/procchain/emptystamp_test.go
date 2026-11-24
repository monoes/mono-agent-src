package procchain

import (
	"os"
	"testing"
)

// Where stamps are supported, a pid with no stamp to compare cannot be told
// from a reused pid, so it is a no (r01-r01-6).
func TestInChainEmptyStampFailsClosedWhereStampsAreSupported(t *testing.T) {
	self := os.Getpid()
	got := InChain([]int{self}, self, "")
	if Supported && got {
		t.Error("an empty stamp matched a pid in the chain")
	}
	if !Supported && !got {
		t.Error("without stamp support the pid alone must still match")
	}
}
