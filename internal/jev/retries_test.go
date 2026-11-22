package jev

import (
	"context"
	"testing"
)

// budgetSeen is a backend that records the retry budget each call carries.
type budgetSeen struct {
	listing
	got []int
	ok  []bool
}

func (b *budgetSeen) Answer(ctx context.Context, req *Request) (*Response, error) {
	n, ok := RetriesFrom(ctx)
	b.got, b.ok = append(b.got, n), append(b.ok, ok)
	return b.listing.Answer(ctx, req)
}

func (b *budgetSeen) Models(ctx context.Context) ([]Model, error) {
	n, ok := RetriesFrom(ctx)
	b.got, b.ok = append(b.got, n), append(b.ok, ok)
	return b.listing.Models(ctx)
}

// Client.Retries reaches a backend through the context, for Ask and Models:
// the default 2, or what the caller set (0 and below = one attempt).
func TestTheRetryBudgetReachesTheBackend(t *testing.T) {
	b := &budgetSeen{}
	b.answer = func(*Request) (*Response, error) {
		return &Response{Answers: map[string]Answer{"q": {Type: TypeNoul, Noul: 0.5}}}, nil
	}
	c := NewBackendClient("p", "m", b)
	q := map[string]Question{"q": {Type: TypeNoul}}
	for _, set := range []int{2, 0, 1, -3} {
		if set != 2 {
			c.Retries = set
		}
		if _, err := c.Ask(context.Background(), nil, q); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Models(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	want := []int{2, 2, 0, 0, 1, 1, 0, 0}
	for i := range want {
		if !b.ok[i] || b.got[i] != want[i] {
			t.Fatalf("budgets %v %v, want %v", b.got, b.ok, want)
		}
	}
	if _, ok := RetriesFrom(context.Background()); ok {
		t.Error("a bare context carries a budget")
	}
}
