//go:build linux

package stop

import (
	"context"
	"errors"
	"testing"
)

func (r *seqRig) writeTwo(ctx context.Context) error {
	if _, err := r.st.AppendAudit(ctx, "p", 1, "a", "x"); err != nil {
		return err
	}
	_, err := r.st.AppendAudit(ctx, "p", 1, "b", "x")
	return err
}

// A transition of two chained rows in one transaction that dies after the database commit is a commit (row 3), because
// the pending line covers both rows.
func TestCrashAfterAMultiRowCommitIsRepairedAsRow3(t *testing.T) {
	r := newSeqRig(t)
	err := r.seq.SequenceRows(bg, 1, "strategy", false, false, 2, func(ctx context.Context) error {
		e := r.writeTwo(ctx)
		r.fs.failAppend = true
		return e
	})
	r.fs.failAppend = false
	mustNoErr(t, err)
	mustNoErr(t, r.seq.Check(bg, 1))
	if got := r.types(t); got[len(got)-1] != RecCommitted {
		t.Fatalf("not repaired: %v", got)
	}
	mustNoErr(t, r.seq.SequenceRows(bg, 1, "strategy", false, false, 2, r.writeTwo))
	mustNoErr(t, r.seq.Check(bg, 1))
}

// The one-row Sequence over a two-row write is exactly the unwitnessed writer: after such a crash the database is ahead of
// the pending range and the generation fails closed at row 5. This is what SequenceRows exists to avoid.
func TestMultiRowWriteUnderAOneRowSequenceFailsClosedAfterACrash(t *testing.T) {
	r := newSeqRig(t)
	_ = r.seq.Sequence(bg, 1, "strategy", false, false, func(ctx context.Context) error {
		e := r.writeTwo(ctx)
		r.fs.failAppend = true
		return e
	})
	r.fs.failAppend = false
	var fe *FreshnessError
	if err := r.seq.Check(bg, 1); !errors.As(err, &fe) || fe.Row != 5 {
		t.Fatalf("want a row 5 failure, got %v", err)
	}
}

// A call that writes nothing is abandoned, whatever row count it declared.
func TestMultiRowSequenceWithNoRowsIsAbandoned(t *testing.T) {
	r := newSeqRig(t)
	boom := errors.New("refused")
	if err := r.seq.SequenceRows(bg, 1, "strategy", false, false, 2, func(context.Context) error { return boom }); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if got := r.types(t); got[len(got)-1] != RecAbandoned {
		t.Fatalf("%v", got)
	}
	mustNoErr(t, r.seq.Check(bg, 1))
}
