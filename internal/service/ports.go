package service

import (
	"context"

	"github.com/saumya712/ledgerlite/internal/model"
)

// These interfaces are the "ports": what the service NEEDS from the outside world.
// They live here, next to the code that uses them. The repository package
// implements them, and main.go connects the two. The service never imports
// the repository.
//
// Each one is small and does one job, so a test fake for any of them is tiny.

// AccountRepo reads and updates accounts inside a transaction.
//
// Contract (the Postgres version and every test fake must follow it):
//   - LockForUpdate locks the rows in ASCENDING id order, no matter what order
//     the ids were passed in. Two transfers locking the same accounts in the
//     same order can never deadlock each other.
//   - LockForUpdate returns model.ErrAccountNotFound if any id does not exist.
//   - AdjustBalance adds delta (positive or negative) to the stored balance.
type AccountRepo interface {
	LockForUpdate(ctx context.Context, ids ...string) (map[string]model.Account, error)
	AdjustBalance(ctx context.Context, id string, delta int64) error
}

// TransferRepo appends history. Rows are never updated or deleted.
type TransferRepo interface {
	InsertTransfer(ctx context.Context, t model.Transfer) (transferID string, err error)
	InsertEntries(ctx context.Context, transferID string, entries []model.Entry) error
}

// IdempotencyClaim is the result of trying to claim an idempotency key.
type IdempotencyClaim struct {
	Replay   bool   // true: this key was already completed, do not move money again
	Response []byte // the stored response, only meaningful when Replay is true
}

// IdempotencyRepo makes retries safe.
//
// Contract:
//   - Claim inserts the key. If another transaction is already using the same
//     key, it waits until that transaction finishes, then reports the outcome.
//   - Claim returns model.ErrIdempotencyMismatch if the key exists with a
//     different requestHash (same key, different request = a client bug).
type IdempotencyRepo interface {
	Claim(ctx context.Context, clientID, key, requestHash string) (IdempotencyClaim, error)
	Complete(ctx context.Context, clientID, key, transferID string, response []byte) error
}

// OutboxRepo records an event in the same transaction as the money movement,
// so an event exists if and only if the transfer committed.
type OutboxRepo interface {
	Add(ctx context.Context, aggregateID, eventType string, payload []byte) error
}

// Tx groups the repositories that share ONE database transaction.
// Everything you call through a Tx either commits together or rolls back together.
type Tx interface {
	Accounts() AccountRepo
	Transfers() TransferRepo
	Idempotency() IdempotencyRepo
	Outbox() OutboxRepo
}

// TxManager runs fn inside one transaction: COMMIT if fn returns nil,
// ROLLBACK if it returns an error. The service never writes BEGIN or COMMIT.
type TxManager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
}
