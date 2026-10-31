package procbind

import (
	"bytes"
	"context"
	"testing"
)

func TestKeysAreDerivedPerProfileGenerationAndPurpose(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	a1, err := AnswerKey(ctx, db, "p1", "gen-1")
	if err != nil || len(a1) != 32 {
		t.Fatalf("answer key: %d bytes, %v", len(a1), err)
	}
	again, err := AnswerKey(ctx, db, "p1", "gen-1")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a1, again) {
		t.Error("the same inputs gave another key")
	}
	a2, err := AnswerKey(ctx, db, "p1", "gen-2")
	if err != nil || len(a2) != 32 {
		t.Fatalf("second generation: %d bytes, %v", len(a2), err)
	}
	other, err := AnswerKey(ctx, db, "p2", "gen-1")
	if err != nil || len(other) != 32 {
		t.Fatalf("other profile: %d bytes, %v", len(other), err)
	}
	row, err := RowKey(ctx, db, "p1")
	if err != nil || len(row) != 32 {
		t.Fatalf("row key: %d bytes, %v", len(row), err)
	}
	for name, k := range map[string][]byte{"another generation": a2, "another profile": other, "the row key": row} {
		if bytes.Equal(a1, k) {
			t.Errorf("%s has the same key as the answer key", name)
		}
	}
	if bytes.Equal(a2, other) || bytes.Equal(a2, row) || bytes.Equal(other, row) {
		t.Error("two different derivations gave one key")
	}
	// the key is good for what it is for: an answer signed under it verifies, under another generation's it does not
	ans := Answer{NV: nonce(1), NS: nonce(2), Generation: "gen-1", TokenID: "t", Profile: "p1", Scope: "s", Verdict: VerdictAllow}
	mac, err := ans.MAC(a1)
	if err != nil {
		t.Fatal(err)
	}
	if err := ans.Verify(a1, mac); err != nil {
		t.Errorf("an answer did not verify under its own key: %v", err)
	}
	if err := ans.Verify(a2, mac); err == nil {
		t.Error("an answer verified under the next generation's key")
	}
}

func TestKeysRefuseEmptyInputs(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	if _, err := AnswerKey(ctx, db, "", "gen"); err == nil {
		t.Error("an empty profile was accepted")
	}
	if _, err := AnswerKey(ctx, db, "p1", ""); err == nil {
		t.Error("an empty generation was accepted")
	}
	if _, err := RowKey(ctx, db, ""); err == nil {
		t.Error("an empty profile was accepted for the row key")
	}
}
