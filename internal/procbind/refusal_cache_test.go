package procbind

import (
	"errors"
	"testing"
	"time"
)

// The machine-capability probe is reused for a few seconds (it is expensive on Windows), a cause is not kept longer than that,
// and what is not the machine's (the service running, the build's issuance) is never cached. Revert proof: calling ready()
// directly in Refusal makes the call count equal the number of asks (red).
func TestRefusalCachesTheMachineProbeBrieflyAndNeverTheRest(t *testing.T) {
	ForTestSetIssuance(t, true)
	now := time.Unix(1000, 0)
	ForTestSetReadyClock(t, func() time.Time { return now })
	calls := 0
	var probeErr error
	ForTestSetReadyFunc(t, func() error { calls++; return probeErr })

	for i := 0; i < 3; i++ {
		if got := Refusal(true); got != "" {
			t.Fatalf("ask %d: %q", i, got)
		}
	}
	if calls != 1 {
		t.Fatalf("3 asks within the TTL ran the probe %d times", calls)
	}
	if Refusal(false) != "its service is not running" || calls != 1 { // the service's state is the caller's, never cached
		t.Fatalf("service state or probe count wrong: %d", calls)
	}
	now = now.Add(readyTTL + time.Second)
	probeErr = errors.New("no temp dir")
	if got := Refusal(true); got != "this machine cannot run it: no temp dir" || calls != 2 {
		t.Fatalf("after the TTL: %q calls=%d", got, calls)
	}
	probeErr = nil // a cause lasts no longer than the TTL
	if got := Refusal(true); got == "" || calls != 2 {
		t.Fatalf("a cause must be reused within the TTL: %q calls=%d", got, calls)
	}
	now = now.Add(readyTTL)
	if got := Refusal(true); got != "" || calls != 3 {
		t.Fatalf("a recovered machine is seen after the TTL: %q calls=%d", got, calls)
	}
}
