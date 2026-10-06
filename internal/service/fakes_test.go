package service

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/saumya712/ledgerlite/internal/model"
)

// In-memory fakes for the ports in ports.go. They follow the same contracts
// as the real Postgres repositories, so the service behaves the same with either.

const fakeExternalID = "external"

type fakeIdem struct {
	hash       string
	transferID string
	response   []byte
	completed  bool
}

type fakeOutboxEvent struct {
	AggregateID string
	Type        string
	Payload     []byte
}

type fakeState struct {
	accounts  map[string]model.Account
	transfers []model.Transfer
	entries   []model.Entry
	idem      map[string]fakeIdem
	outbox    []fakeOutboxEvent
}

func (s fakeState) clone() fakeState {
	c := fakeState{
		accounts:  make(map[string]model.Account, len(s.accounts)),
		transfers: append([]model.Transfer(nil), s.transfers...),
		entries:   append([]model.Entry(nil), s.entries...),
		idem:      make(map[string]fakeIdem, len(s.idem)),
		outbox:    append([]fakeOutboxEvent(nil), s.outbox...),
	}
	for k, v := range s.accounts {
		c.accounts[k] = v
	}
	for k, v := range s.idem {
		c.idem[k] = v
	}
	return c
}

// fakeDB implements TxManager. A mutex held for the whole transaction makes
// transactions run one at a time, and a snapshot is restored on error to
// imitate ROLLBACK.
type fakeDB struct {
	mu    sync.Mutex
	state fakeState
}

func newFakeDB() *fakeDB {
	db := &fakeDB{state: fakeState{
		accounts: map[string]model.Account{},
		idem:     map[string]fakeIdem{},
	}}
	db.state.accounts[fakeExternalID] = model.Account{
		ID: fakeExternalID, Name: "external", Kind: model.KindExternal, Currency: "INR",
	}
	return db
}

func (db *fakeDB) WithinTx(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	snapshot := db.state.clone()
	if err := fn(ctx, fakeTx{db: db}); err != nil {
		db.state = snapshot // ROLLBACK
		return err
	}
	return nil // COMMIT
}

// ---- helpers used by tests (not part of any port) ----

func (db *fakeDB) seedUser(id string, balance int64) {
	db.state.accounts[id] = model.Account{
		ID: id, Name: id, Kind: model.KindUser, Currency: "INR", Balance: balance,
	}
}

func (db *fakeDB) balance(id string) int64 { return db.state.accounts[id].Balance }
func (db *fakeDB) transferCount() int      { return len(db.state.transfers) }
func (db *fakeDB) entryCount() int         { return len(db.state.entries) }
func (db *fakeDB) outboxCount() int        { return len(db.state.outbox) }

func (db *fakeDB) totalBalance() int64 {
	var total int64
	for _, a := range db.state.accounts {
		total += a.Balance
	}
	return total
}

func (db *fakeDB) entriesSum() int64 {
	var sum int64
	for _, e := range db.state.entries {
		sum += e.Amount
	}
	return sum
}

// ---- Tx ----

type fakeTx struct{ db *fakeDB }

func (tx fakeTx) Accounts() AccountRepo        { return fakeAccounts{tx.db} }
func (tx fakeTx) Transfers() TransferRepo      { return fakeTransfers{tx.db} }
func (tx fakeTx) Idempotency() IdempotencyRepo { return fakeIdempotency{tx.db} }
func (tx fakeTx) Outbox() OutboxRepo           { return fakeOutbox{tx.db} }

// ---- AccountRepo ----

type fakeAccounts struct{ db *fakeDB }

func (r fakeAccounts) LockForUpdate(ctx context.Context, ids ...string) (map[string]model.Account, error) {
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted) // contract: ascending id order (it matters for the real DB)
	out := make(map[string]model.Account, len(ids))
	for _, id := range sorted {
		a, ok := r.db.state.accounts[id]
		if !ok {
			return nil, model.ErrAccountNotFound
		}
		out[id] = a
	}
	return out, nil
}

func (r fakeAccounts) AdjustBalance(ctx context.Context, id string, delta int64) error {
	a, ok := r.db.state.accounts[id]
	if !ok {
		return model.ErrAccountNotFound
	}
	a.Balance += delta
	// Imitates the database CHECK constraint, the last line of defence.
	if a.Kind == model.KindUser && a.Balance < 0 {
		return fmt.Errorf("check constraint balance_non_negative violated for %s", id)
	}
	r.db.state.accounts[id] = a
	return nil
}

// ---- TransferRepo ----

type fakeTransfers struct{ db *fakeDB }

func (r fakeTransfers) InsertTransfer(ctx context.Context, t model.Transfer) (string, error) {
	t.ID = fmt.Sprintf("t%d", len(r.db.state.transfers)+1)
	r.db.state.transfers = append(r.db.state.transfers, t)
	return t.ID, nil
}

func (r fakeTransfers) InsertEntries(ctx context.Context, transferID string, entries []model.Entry) error {
	var sum int64
	for _, e := range entries {
		sum += e.Amount
	}
	// Imitates the deferred constraint trigger: entries of one transfer sum to zero.
	if sum != 0 {
		return fmt.Errorf("entries for transfer %s do not sum to zero", transferID)
	}
	for _, e := range entries {
		e.TransferID = transferID
		r.db.state.entries = append(r.db.state.entries, e)
	}
	return nil
}

// ---- IdempotencyRepo ----

type fakeIdempotency struct{ db *fakeDB }

func (r fakeIdempotency) Claim(ctx context.Context, clientID, key, requestHash string) (IdempotencyClaim, error) {
	k := clientID + "/" + key
	existing, ok := r.db.state.idem[k]
	if !ok {
		r.db.state.idem[k] = fakeIdem{hash: requestHash}
		return IdempotencyClaim{}, nil
	}
	if existing.hash != requestHash {
		return IdempotencyClaim{}, model.ErrIdempotencyMismatch
	}
	return IdempotencyClaim{Replay: true, Response: existing.response}, nil
}

func (r fakeIdempotency) Complete(ctx context.Context, clientID, key, transferID string, response []byte) error {
	k := clientID + "/" + key
	e, ok := r.db.state.idem[k]
	if !ok {
		return fmt.Errorf("Complete called without Claim for key %q", key)
	}
	e.transferID = transferID
	e.response = response
	e.completed = true
	r.db.state.idem[k] = e
	return nil
}

// ---- OutboxRepo ----

type fakeOutbox struct{ db *fakeDB }

func (r fakeOutbox) Add(ctx context.Context, aggregateID, eventType string, payload []byte) error {
	r.db.state.outbox = append(r.db.state.outbox, fakeOutboxEvent{aggregateID, eventType, payload})
	return nil
}

// Compile-time checks: if a fake drifts from its port, the build fails.
var (
	_ TxManager       = (*fakeDB)(nil)
	_ Tx              = fakeTx{}
	_ AccountRepo     = fakeAccounts{}
	_ TransferRepo    = fakeTransfers{}
	_ IdempotencyRepo = fakeIdempotency{}
	_ OutboxRepo      = fakeOutbox{}
)
