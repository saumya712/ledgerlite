
// Integration tests: real Postgres, real repository, real service.
// They prove the properties the fakes cannot: locking, deadlock avoidance,
// idempotency under concurrency, and rollback.
//
// Run with:  docker compose up -d   then   go test ./test/ -v -count=1
// Tests share one database and truncate it, so they run one at a time.
package test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/saumya712/ledgerlite/internal/model"
	"github.com/saumya712/ledgerlite/internal/repository"
	"github.com/saumya712/ledgerlite/internal/service"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://ledger:ledger@localhost:5432/ledgerlite"
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bad DATABASE_URL:", err)
		os.Exit(1)
	}
	cfg.MaxConns = 25

	pool, err = pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot create pool:", err)
		os.Exit(1)
	}
	if err := pool.Ping(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "cannot reach the database (is `docker compose up -d` running?):", err)
		os.Exit(1)
	}

	code := m.Run()
	pool.Close()
	os.Exit(code)
}

// ---------------------------------------------------------------- helpers

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// bg returns a context with a timeout, so a hang fails the test instead of freezing it.
func bg(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

type env struct {
	svc        *service.Service
	txm        *repository.TxManager
	externalID string
}

// setup wipes all data and recreates the special 'external' account.
func setup(t *testing.T) *env {
	t.Helper()
	ctx := bg(t)

	_, err := pool.Exec(ctx,
		`TRUNCATE entries, transfers, idempotency_keys, outbox, accounts RESTART IDENTITY`)
	must(t, err)

	var extID string
	err = pool.QueryRow(ctx,
		`INSERT INTO accounts (name, kind) VALUES ('external', 'external') RETURNING id::text`).Scan(&extID)
	must(t, err)

	txm := repository.NewTxManager(pool)
	return &env{svc: service.NewService(txm, extID), txm: txm, externalID: extID}
}

// newUser creates a user account and funds it through a real deposit,
// so the books balance from the very first row.
func (e *env) newUser(t *testing.T, name string, balance int64) string {
	t.Helper()
	ctx := bg(t)

	var id string
	must(t, pool.QueryRow(ctx,
		`INSERT INTO accounts (name) VALUES ($1) RETURNING id::text`, name).Scan(&id))

	if balance > 0 {
		_, err := e.svc.Deposit(ctx, service.TransferCommand{
			ClientID: "seed", IdempotencyKey: "seed-" + name, To: id, Amount: balance,
		})
		must(t, err)
	}
	return id
}

func balanceOf(t *testing.T, id string) int64 {
	t.Helper()
	var b int64
	must(t, pool.QueryRow(bg(t),
		`SELECT balance FROM accounts WHERE id = $1::text::uuid`, id).Scan(&b))
	return b
}

func assertBalance(t *testing.T, id string, want int64) {
	t.Helper()
	if got := balanceOf(t, id); got != want {
		t.Errorf("balance = %d, want %d", got, want)
	}
}

func countRows(t *testing.T, table string) int64 {
	t.Helper()
	var n int64
	must(t, pool.QueryRow(bg(t), "SELECT COUNT(*) FROM "+table).Scan(&n))
	return n
}

// assertInvariants checks the three rules that must hold after every scenario.
func assertInvariants(t *testing.T) {
	t.Helper()
	ctx := bg(t)

	// 1. Money is never created or destroyed: all entries sum to zero.
	var sum int64
	must(t, pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount), 0)::bigint FROM entries`).Scan(&sum))
	if sum != 0 {
		t.Errorf("INVARIANT 1 broken: entries sum to %d, want 0", sum)
	}

	// 2. Every account's stored balance equals the sum of its entries.
	var drift int64
	must(t, pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM accounts a
		WHERE a.balance <> COALESCE((SELECT SUM(e.amount) FROM entries e WHERE e.account_id = a.id), 0)`).Scan(&drift))
	if drift != 0 {
		t.Errorf("INVARIANT 2 broken: %d account(s) whose balance does not match their entries", drift)
	}

	// 3. No transfer has entries that fail to sum to zero.
	var unbalanced int64
	must(t, pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM (
			SELECT transfer_id FROM entries GROUP BY transfer_id HAVING SUM(amount) <> 0
		) x`).Scan(&unbalanced))
	if unbalanced != 0 {
		t.Errorf("INVARIANT 3 broken: %d transfer(s) with unbalanced entries", unbalanced)
	}
}

// runConcurrently starts n goroutines and releases them at the same instant.
func runConcurrently(n int, fn func(i int)) {
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			fn(i)
		}(i)
	}
	close(start)
	wg.Wait()
}

// ---------------------------------------------------------------- tests

// 50 requests each try to send Rs 30 from an account holding Rs 100.
// Exactly 3 can succeed (Rs 90). A race would let more through.
func TestConcurrentTransfers_NoDoubleSpend(t *testing.T) {
	e := setup(t)
	ctx := bg(t)
	alice := e.newUser(t, "alice", 10000)
	bob := e.newUser(t, "bob", 0)

	const n, amount = 50, 3000
	errs := make([]error, n)
	runConcurrently(n, func(i int) {
		_, errs[i] = e.svc.Transfer(ctx, service.TransferCommand{
			ClientID: "c1", IdempotencyKey: fmt.Sprintf("k-%d", i),
			From: alice, To: bob, Amount: amount,
		})
	})

	var ok, insufficient int
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, model.ErrInsufficientFunds):
			insufficient++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 3 || insufficient != n-3 {
		t.Errorf("succeeded=%d insufficient=%d, want 3 and %d", ok, insufficient, n-3)
	}
	assertBalance(t, alice, 1000)
	assertBalance(t, bob, 9000)
	assertInvariants(t)
}

// 100 transfers of Rs 10 from an account holding exactly Rs 1000: all must
// succeed and nothing may be lost. A lost update would leave money behind.
func TestConcurrentTransfers_NoLostUpdates(t *testing.T) {
	e := setup(t)
	ctx := bg(t)
	alice := e.newUser(t, "alice", 100000)
	bob := e.newUser(t, "bob", 0)

	const n, amount = 100, 1000
	errs := make([]error, n)
	runConcurrently(n, func(i int) {
		_, errs[i] = e.svc.Transfer(ctx, service.TransferCommand{
			ClientID: "c1", IdempotencyKey: fmt.Sprintf("k-%d", i),
			From: alice, To: bob, Amount: amount,
		})
	})

	for i, err := range errs {
		if err != nil {
			t.Errorf("transfer %d failed: %v", i, err)
		}
	}
	assertBalance(t, alice, 0)
	assertBalance(t, bob, 100000)
	assertInvariants(t)
}

// The same request (same key) sent 50 times at the same instant:
// money moves once, and everyone gets the same transfer id back.
func TestIdempotency_SameKeyConcurrent(t *testing.T) {
	e := setup(t)
	ctx := bg(t)
	alice := e.newUser(t, "alice", 10000)
	bob := e.newUser(t, "bob", 0)
	transfersBefore := countRows(t, "transfers")

	const n = 50
	results := make([]service.TransferResult, n)
	errs := make([]error, n)
	runConcurrently(n, func(i int) {
		results[i], errs[i] = e.svc.Transfer(ctx, service.TransferCommand{
			ClientID: "c1", IdempotencyKey: "same-key",
			From: alice, To: bob, Amount: 1000,
		})
	})

	executed := 0
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Errorf("request %d failed: %v", i, errs[i])
			continue
		}
		if !results[i].Replayed {
			executed++
		}
		if results[i].TransferID != results[0].TransferID {
			t.Errorf("request %d returned transfer %q, request 0 returned %q",
				i, results[i].TransferID, results[0].TransferID)
		}
	}
	if executed != 1 {
		t.Errorf("%d requests actually executed, want exactly 1", executed)
	}
	if got := countRows(t, "transfers") - transfersBefore; got != 1 {
		t.Errorf("%d transfers recorded, want 1", got)
	}
	assertBalance(t, alice, 9000)
	assertBalance(t, bob, 1000)
	assertInvariants(t)
}

func TestIdempotency_SameKeyDifferentBody(t *testing.T) {
	e := setup(t)
	ctx := bg(t)
	alice := e.newUser(t, "alice", 10000)
	bob := e.newUser(t, "bob", 0)

	_, err := e.svc.Transfer(ctx, service.TransferCommand{
		ClientID: "c1", IdempotencyKey: "k1", From: alice, To: bob, Amount: 1000})
	must(t, err)

	_, err = e.svc.Transfer(ctx, service.TransferCommand{
		ClientID: "c1", IdempotencyKey: "k1", From: alice, To: bob, Amount: 2000})
	if !errors.Is(err, model.ErrIdempotencyMismatch) {
		t.Fatalf("err = %v, want ErrIdempotencyMismatch", err)
	}
	assertBalance(t, alice, 9000) // only the first one applied
	assertBalance(t, bob, 1000)
	assertInvariants(t)
}

// Alice->Bob and Bob->Alice at the same time is the classic deadlock setup.
// Locking in a fixed order means every transfer must succeed.
func TestOppositeTransfers_NoDeadlock(t *testing.T) {
	e := setup(t)
	ctx := bg(t)
	alice := e.newUser(t, "alice", 100000)
	bob := e.newUser(t, "bob", 100000)

	const n = 100
	errs := make([]error, n)
	runConcurrently(n, func(i int) {
		from, to := alice, bob
		if i%2 == 1 {
			from, to = bob, alice
		}
		_, errs[i] = e.svc.Transfer(ctx, service.TransferCommand{
			ClientID: "c1", IdempotencyKey: fmt.Sprintf("k-%d", i),
			From: from, To: to, Amount: 100,
		})
	})

	for i, err := range errs {
		if err != nil {
			t.Errorf("transfer %d failed (deadlock?): %v", i, err)
		}
	}
	// 50 each way, same amount: balances end where they started.
	assertBalance(t, alice, 100000)
	assertBalance(t, bob, 100000)
	assertInvariants(t)
}

func TestInsufficientFunds_LeavesNothingBehind(t *testing.T) {
	e := setup(t)
	ctx := bg(t)
	alice := e.newUser(t, "alice", 5000)
	bob := e.newUser(t, "bob", 0)
	transfersBefore, entriesBefore, outboxBefore :=
		countRows(t, "transfers"), countRows(t, "entries"), countRows(t, "outbox")

	_, err := e.svc.Transfer(ctx, service.TransferCommand{
		ClientID: "c1", IdempotencyKey: "k1", From: alice, To: bob, Amount: 9000})
	if !errors.Is(err, model.ErrInsufficientFunds) {
		t.Fatalf("err = %v, want ErrInsufficientFunds", err)
	}
	if countRows(t, "transfers") != transfersBefore ||
		countRows(t, "entries") != entriesBefore ||
		countRows(t, "outbox") != outboxBefore {
		t.Error("a rejected transfer must not write any rows")
	}
	// The idempotency key was rolled back too, so it can be retried later.
	var keys int64
	must(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM idempotency_keys WHERE key = 'k1'`).Scan(&keys))
	if keys != 0 {
		t.Error("a failed request must not keep its idempotency key")
	}
	assertInvariants(t)
}

// Simulates a crash halfway through a transfer: the debit has been written,
// then the process "dies" before the credit. Everything must roll back.
func TestRollback_PartialWorkLeavesNothing(t *testing.T) {
	e := setup(t)
	ctx := bg(t)
	alice := e.newUser(t, "alice", 10000)
	bob := e.newUser(t, "bob", 0)
	transfersBefore, entriesBefore := countRows(t, "transfers"), countRows(t, "entries")

	errCrash := errors.New("simulated crash")
	err := e.txm.WithinTx(ctx, func(ctx context.Context, tx service.Tx) error {
		id, err := tx.Transfers().InsertTransfer(ctx,
			model.Transfer{FromAccount: alice, ToAccount: bob, Amount: 8000})
		if err != nil {
			return err
		}
		if err := tx.Transfers().InsertEntries(ctx, id, []model.Entry{
			{AccountID: alice, Amount: -8000},
			{AccountID: bob, Amount: 8000},
		}); err != nil {
			return err
		}
		if err := tx.Accounts().AdjustBalance(ctx, alice, -8000); err != nil {
			return err
		}
		return errCrash // dies before bob is credited
	})
	if !errors.Is(err, errCrash) {
		t.Fatalf("err = %v, want the simulated crash", err)
	}

	assertBalance(t, alice, 10000)
	assertBalance(t, bob, 0)
	if countRows(t, "transfers") != transfersBefore || countRows(t, "entries") != entriesBefore {
		t.Error("partial work was not rolled back")
	}
	assertInvariants(t)
}

// Even if the Go code is buggy, the database itself refuses unbalanced entries.
func TestDatabaseRejectsUnbalancedEntries(t *testing.T) {
	e := setup(t)
	ctx := bg(t)
	alice := e.newUser(t, "alice", 10000)
	bob := e.newUser(t, "bob", 0)
	transfersBefore := countRows(t, "transfers")

	err := e.txm.WithinTx(ctx, func(ctx context.Context, tx service.Tx) error {
		id, err := tx.Transfers().InsertTransfer(ctx,
			model.Transfer{FromAccount: alice, ToAccount: bob, Amount: 100})
		if err != nil {
			return err
		}
		// -100 and +50 do not sum to zero. Rejected when the transaction commits.
		return tx.Transfers().InsertEntries(ctx, id, []model.Entry{
			{AccountID: alice, Amount: -100},
			{AccountID: bob, Amount: 50},
		})
	})
	if err == nil {
		t.Fatal("unbalanced entries must be rejected at commit")
	}
	if countRows(t, "transfers") != transfersBefore {
		t.Error("the rejected transfer must not be stored")
	}
	assertInvariants(t)
}

// History cannot be rewritten, even with direct SQL.
func TestLedgerRowsAreImmutable(t *testing.T) {
	e := setup(t)
	ctx := bg(t)
	e.newUser(t, "alice", 10000) // the deposit creates entries to try to modify

	if _, err := pool.Exec(ctx, `UPDATE entries SET amount = amount + 1`); err == nil {
		t.Error("UPDATE on entries should be rejected")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM entries`); err == nil {
		t.Error("DELETE on entries should be rejected")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM transfers`); err == nil {
		t.Error("DELETE on transfers should be rejected")
	}
	assertInvariants(t)
}

