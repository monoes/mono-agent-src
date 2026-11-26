package jevcli

import (
	"context"
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
)

// Retry triage is sure of a sampled class above its reach only when all five
// samples agree.
func TestRetryTriageGatesASampledClassByUnanimity(t *testing.T) {
	for _, agree := range []int{5, 4} {
		a, err := jevtest.Sampled(retryQuestion, "permanent", agree)
		if err != nil {
			t.Fatal(err)
		}
		class, sure := retrySure(&jev.Response{Probability: jev.ProbSampled}, a, 0.9)
		if class != "permanent" || sure != (agree == 5) {
			t.Errorf("%d of 5: %q, sure=%v", agree, class, sure)
		}
	}
}

// A sampled provider stops a retry under a threshold it cannot read only when
// all five samples agree.
func TestASampledRetryTriageNeedsAUnanimousSample(t *testing.T) {
	db := retryTestDB(t)
	fb := jevtest.NewFakeBackend(nil)
	registerFake(t, fb)
	if err := jevconf.SaveProvider(context.Background(), db, jevconf.Provider{Name: "local", Kind: fakeKind, BaseURL: "http://localhost"}); err != nil {
		t.Fatal(err)
	}
	if err := jevconf.SetSurfaceProvider(context.Background(), db, "p1", jevconf.Retry, "local"); err != nil {
		t.Fatal(err)
	}
	if err := jevconf.SetEnabled(db, "p1", jevconf.Retry, true); err != nil {
		t.Fatal(err)
	}
	if err := jevconf.SetThreshold(db, "p1", jevconf.Retry, 0.9); err != nil {
		t.Fatal(err)
	}
	for _, agree := range []int{5, 4} {
		fb.SetSampled(jevtest.Fixed(map[string]string{retryQuestionID: "permanent"}), agree)
		retry, _ := jevRetryClassifier(retryCtx(db, "p1"), "x", errors.New("e"), 1)
		if retry != (agree == 4) {
			t.Errorf("%d of 5: retry = %v", agree, retry)
		}
	}
}
