package service

import (
	"context"
	"errors"
	"testing"

	"github.com/saumya712/ledgerlite/internal/model"
)

func cmd(key string, amount int64) TransferCommand {
	return TransferCommand{ClientID: "c1", IdempotencyKey: key, From: "alice", To: "bob", Amount: amount}
}

func setup(t *testing.T) (*Service, *fakeDB) {
	t.Helper()
	db := newFakeDB()
	db.seedUser("alice", 10000) // Rs 100.00
	db.seedUser("bob", 0)
	return NewService(db, fakeExternalID), db
}

func assertBalance(t *testing.T, db *fakeDB, id string, want int64) {
	t.Helper()
	if got := db.balance(id); got != want {
		t.Errorf("balance of %s = %d, want %d", id, got, want)
	}
}

// Money is never created or destroyed: total unchanged, entries sum to zero.
func assertConserved(t *testing.T, db *fakeDB, totalBefore int64) {
	t.Helper()
	if got := db.totalBalance(); got != totalBefore {
		t.Errorf("total balance changed: %d -> %d", totalBefore, got)
	}
	if got := db.entriesSum(); got != 0 {
		t.Errorf("entries sum = %d, want 0", got)
	}
}

func TestTransfer_Success(t *testing.T) {
	svc, db := setup(t)
	before := db.totalBalance()

	res, err := svc.Transfer(context.Background(), cmd("k1", 8000))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.TransferID == "" || res.Replayed {
		t.Fatalf("unexpected result: %+v", res)
	}
	assertBalance(t, db, "alice", 2000)
	assertBalance(t, db, "bob", 8000)
	assertConserved(t, db, before)
	if db.transferCount() != 1 || db.entryCount() != 2 || db.outboxCount() != 1 {
		t.Errorf("rows: transfers=%d entries=%d outbox=%d, want 1/2/1",
			db.transferCount(), db.entryCount(), db.outboxCount())
	}
}

func TestTransfer_InsufficientFunds(t *testing.T) {
	svc, db := setup(t)
	before := db.totalBalance()

	_, err := svc.Transfer(context.Background(), cmd("k1", 15000)) // alice only has 10000
	if !errors.Is(err, model.ErrInsufficientFunds) {
		t.Fatalf("err = %v, want ErrInsufficientFunds", err)
	}
	assertBalance(t, db, "alice", 10000)
	assertBalance(t, db, "bob", 0)
	assertConserved(t, db, before)
	if db.transferCount() != 0 || db.entryCount() != 0 || db.outboxCount() != 0 {
		t.Error("a failed transfer must leave no rows behind")
	}
}

func TestTransfer_DuplicateKeyMovesMoneyOnce(t *testing.T) {
	svc, db := setup(t)
	ctx := context.Background()

	first, err := svc.Transfer(ctx, cmd("k1", 8000))
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := svc.Transfer(ctx, cmd("k1", 8000)) // client retry
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !second.Replayed {
		t.Error("second call should be marked Replayed")
	}
	if second.TransferID != first.TransferID {
		t.Errorf("replay returned transfer %q, want %q", second.TransferID, first.TransferID)
	}
	assertBalance(t, db, "alice", 2000) // charged once, not twice
	assertBalance(t, db, "bob", 8000)
	if db.transferCount() != 1 || db.entryCount() != 2 || db.outboxCount() != 1 {
		t.Errorf("a replay must not add rows: transfers=%d entries=%d outbox=%d",
			db.transferCount(), db.entryCount(), db.outboxCount())
	}
}

func TestTransfer_SameKeyDifferentRequest(t *testing.T) {
	svc, db := setup(t)
	ctx := context.Background()

	if _, err := svc.Transfer(ctx, cmd("k1", 8000)); err != nil {
		t.Fatalf("first: %v", err)
	}
	_, err := svc.Transfer(ctx, cmd("k1", 9000)) // same key, different amount
	if !errors.Is(err, model.ErrIdempotencyMismatch) {
		t.Fatalf("err = %v, want ErrIdempotencyMismatch", err)
	}
	assertBalance(t, db, "alice", 2000) // only the first transfer applied
	assertBalance(t, db, "bob", 8000)
}

// A failed request is rolled back, including its idempotency key, so the
// client can retry with the same key once the problem is fixed.
func TestTransfer_FailureDoesNotBurnTheKey(t *testing.T) {
	svc, db := setup(t)
	ctx := context.Background()

	if _, err := svc.Transfer(ctx, cmd("k2", 15000)); !errors.Is(err, model.ErrInsufficientFunds) {
		t.Fatalf("err = %v, want ErrInsufficientFunds", err)
	}
	db.seedUser("alice", 20000) // pretend alice got more money

	res, err := svc.Transfer(ctx, cmd("k2", 15000))
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if res.Replayed {
		t.Error("retry after a failure must execute, not replay")
	}
	assertBalance(t, db, "alice", 5000)
	assertBalance(t, db, "bob", 15000)
}

func TestTransfer_UnknownAccount(t *testing.T) {
	svc, db := setup(t)
	c := cmd("k1", 1000)
	c.To = "nobody"

	_, err := svc.Transfer(context.Background(), c)
	if !errors.Is(err, model.ErrAccountNotFound) {
		t.Fatalf("err = %v, want ErrAccountNotFound", err)
	}
	assertBalance(t, db, "alice", 10000)
	if db.transferCount() != 0 {
		t.Error("no transfer should be recorded")
	}
}

func TestTransfer_InvalidInput(t *testing.T) {
	cases := []struct {
		name string
		c    TransferCommand
		want error
	}{
		{"zero amount", TransferCommand{ClientID: "c1", IdempotencyKey: "k", From: "alice", To: "bob", Amount: 0}, model.ErrInvalidAmount},
		{"negative amount", TransferCommand{ClientID: "c1", IdempotencyKey: "k", From: "alice", To: "bob", Amount: -500}, model.ErrInvalidAmount},
		{"same account", TransferCommand{ClientID: "c1", IdempotencyKey: "k", From: "alice", To: "alice", Amount: 100}, model.ErrSameAccount},
		{"missing key", TransferCommand{ClientID: "c1", IdempotencyKey: "", From: "alice", To: "bob", Amount: 100}, model.ErrMissingIdempotencyKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, db := setup(t)
			_, err := svc.Transfer(context.Background(), tc.c)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			assertBalance(t, db, "alice", 10000)
			if db.transferCount() != 0 {
				t.Error("no transfer should be recorded")
			}
		})
	}
}

func TestDeposit(t *testing.T) {
	svc, db := setup(t)
	before := db.totalBalance()

	c := TransferCommand{ClientID: "c1", IdempotencyKey: "d1", To: "alice", Amount: 5000}
	if _, err := svc.Deposit(context.Background(), c); err != nil {
		t.Fatalf("deposit: %v", err)
	}
	assertBalance(t, db, "alice", 15000)
	assertBalance(t, db, fakeExternalID, -5000) // money entered the system from 'external'
	assertConserved(t, db, before)
}
func TestTransfer_RejectsExternalAccount(t *testing.T) {
	svc, db := setup(t)
	before := db.totalBalance()

	from := cmd("k1", 1000)
	from.From = fakeExternalID
	if _, err := svc.Transfer(context.Background(), from); !errors.Is(err, model.ErrExternalAccount) {
		t.Fatalf("from external: err = %v, want ErrExternalAccount", err)
	}

	to := cmd("k2", 1000)
	to.To = fakeExternalID
	if _, err := svc.Transfer(context.Background(), to); !errors.Is(err, model.ErrExternalAccount) {
		t.Fatalf("to external: err = %v, want ErrExternalAccount", err)
	}

	assertBalance(t, db, "alice", 10000)
	assertConserved(t, db, before)
}