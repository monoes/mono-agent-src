package chat

import (
	"context"
	"testing"
)

type plainActiveStore struct{ calls int }

func (p *plainActiveStore) SetWorkflowActive(context.Context, string, bool) error {
	p.calls++
	return nil
}

type notedActiveStore struct{ plainActiveStore }

func (n *notedActiveStore) SetWorkflowActiveNoted(context.Context, string, bool) (string, error) {
	return "the file was left as it is", nil
}

// A store that can say it only switched the rows off is asked in a way that lets it say so.
func TestSetActiveNotedPassesTheStoresNoteOn(t *testing.T) {
	if note, err := setActiveNoted(context.Background(), &notedActiveStore{}, "wf", false); err != nil || note == "" {
		t.Fatalf("%q %v", note, err)
	}
	p := &plainActiveStore{}
	if note, err := setActiveNoted(context.Background(), p, "wf", false); err != nil || note != "" || p.calls != 1 {
		t.Fatalf("%q %v %d", note, err, p.calls)
	}
}
