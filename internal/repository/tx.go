package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/saumya712/ledgerlite/internal/service"
)

// dbtx is the small slice of pgx that the repositories need. A pgx transaction
// satisfies it, so every repository inside one transaction shares that transaction.
type dbtx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// TxManager implements service.TxManager.
type TxManager struct {
	pool *pgxpool.Pool
}

func NewTxManager(pool *pgxpool.Pool) *TxManager {
	return &TxManager{pool: pool}
}

// WithinTx runs fn in one transaction: COMMIT if fn returns nil, ROLLBACK otherwise.
// It uses Postgres's default isolation level (READ COMMITTED). Correctness comes
// from the row locks taken in AccountRepo.LockForUpdate, not from a stricter level.
func (m *TxManager) WithinTx(ctx context.Context, fn func(ctx context.Context, tx service.Tx) error) error {
	pgxTx, err := m.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	// Safety net: if we return before Commit (error or panic), roll back.
	// After a successful Commit this is a harmless no-op.
	defer func() { _ = pgxTx.Rollback(context.WithoutCancel(ctx)) }()

	if err := fn(ctx, &txRepos{db: pgxTx}); err != nil {
		return err
	}
	// The deferred "entries sum to zero" constraint trigger fires here, at COMMIT.
	if err := pgxTx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// txRepos implements service.Tx: it hands out repositories bound to one transaction.
type txRepos struct{ db dbtx }

func (t *txRepos) Accounts() service.AccountRepo       { return &accountRepo{db: t.db} }
func (t *txRepos) Transfers() service.TransferRepo     { return &transferRepo{db: t.db} }
func (t *txRepos) Idempotency() service.IdempotencyRepo { return &idempotencyRepo{db: t.db} }
func (t *txRepos) Outbox() service.OutboxRepo          { return &outboxRepo{db: t.db} }

// Compile-time checks: if a repository drifts from its port, the build fails.
var (
	_ service.TxManager       = (*TxManager)(nil)
	_ service.Tx              = (*txRepos)(nil)
	_ service.AccountRepo     = (*accountRepo)(nil)
	_ service.TransferRepo    = (*transferRepo)(nil)
	_ service.IdempotencyRepo = (*idempotencyRepo)(nil)
	_ service.OutboxRepo      = (*outboxRepo)(nil)
)