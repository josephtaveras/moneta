package store

import (
	"context"
	"testing"
)

func TestCorrectionsAreAtomicAndUseCategorySemantics(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	_, err := db.Exec(`INSERT INTO entities(id,kind,name)VALUES(1,'personal','Demo');
 INSERT INTO accounts(id,entity_id,type,name,provider,provider_account_id)VALUES(1,1,'checking','Demo','simplefin','a');
 INSERT INTO transactions(id,account_id,entity_id,date,amount_cents,status,dedup_hash)VALUES(1,1,1,'2026-09-01',-100,'posted','fake');`)
	if err != nil {
		t.Fatal(err)
	}
	var transferCategory int64
	if err := db.QueryRow(`SELECT id FROM categories WHERE kind='transfer' LIMIT 1`).Scan(&transferCategory); err != nil {
		t.Fatal(err)
	}
	if err := CorrectTransaction(ctx, db, 1, TransactionCorrection{CategoryID: &transferCategory}); err != nil {
		t.Fatal(err)
	}
	var transfer, excluded, manual bool
	check := func(wantTransfer, wantExcluded bool) {
		t.Helper()
		if err := db.QueryRow(`SELECT is_transfer,excluded,classification_manual FROM transactions WHERE id=1`).Scan(&transfer, &excluded, &manual); err != nil {
			t.Fatal(err)
		}
		if transfer != wantTransfer || excluded != wantExcluded || !manual {
			t.Fatalf("classification %v %v %v", transfer, excluded, manual)
		}
	}
	check(true, true)
	no := false
	if err := CorrectTransaction(ctx, db, 1, TransactionCorrection{Excluded: &no}); err == nil {
		t.Fatal("included a transfer")
	}
	check(true, true)
	invalid := int64(999999)
	if err := CorrectTransaction(ctx, db, 1, TransactionCorrection{CategoryID: &invalid}); err == nil {
		t.Fatal("accepted nonexistent category")
	}
	check(true, true)
	// Clearing the transfer flag leaves exclusion alone; inclusion is explicit.
	if err := CorrectTransaction(ctx, db, 1, TransactionCorrection{Transfer: &no}); err != nil {
		t.Fatal(err)
	}
	check(false, true)
	if err := CorrectTransaction(ctx, db, 1, TransactionCorrection{Excluded: &no}); err != nil {
		t.Fatal(err)
	}
	check(false, false)
	yes := true
	if err := CorrectTransaction(ctx, db, 1, TransactionCorrection{Transfer: &yes}); err != nil {
		t.Fatal(err)
	}
	check(true, true)
	// Corrections never run or invalidate recurring detection, which stays paused.
	var state string
	if err := db.QueryRow(`SELECT status FROM detector_state`).Scan(&state); err != nil || state != "never_run" {
		t.Fatalf("detector state = %q, %v; want untouched never_run", state, err)
	}
}
