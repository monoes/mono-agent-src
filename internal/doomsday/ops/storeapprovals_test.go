package ops

import (
	"errors"
	"testing"
)

func TestStoreApprovalsReadsTheRecordAndTheChainHead(t *testing.T) {
	f := newRealFix(t, typedStoreCharter())
	ap := StoreApprovals{Store: f.st}
	r := f.issue(t, TypedRequest{Op: OpHostSetting, Setting: &SettingArgs{Key: "theme", Value: "dark"}}, 5)
	rec, err := ap.Get(bg, r.ApprovalID)
	if err != nil || rec.ID != r.ApprovalID || rec.Op != OpHostSetting || rec.Caller != 7 || rec.Profile != "p" || rec.Generation != 1 ||
		rec.CharterID != f.act.ID || rec.CharterHash != f.act.Hash || rec.Revoked || rec.ArgsHash == "" {
		t.Fatalf("%+v %v", rec, err)
	}
	seq, err := ap.Seq(bg, "p")
	if err != nil || seq == 0 || rec.ExpiresSeq != seq+5 {
		t.Fatalf("seq %d expires %d err %v", seq, rec.ExpiresSeq, err)
	}
	if err := f.st.RescindApproval(bg, "p", 1, r.ApprovalID, "boss"); err != nil {
		t.Fatal(err)
	}
	if rec, _ = ap.Get(bg, r.ApprovalID); !rec.Revoked {
		t.Fatal("a rescinded approval is read as live")
	}
	if after, _ := ap.Seq(bg, "p"); after <= seq {
		t.Fatalf("the rescind did not advance the head: %d -> %d", seq, after)
	}
	if _, err := ap.Get(bg, "ap-nope"); !errors.Is(err, ErrUnknownApproval) {
		t.Fatalf("unknown: %v", err)
	}
}
