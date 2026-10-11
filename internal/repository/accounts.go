package repository

import (
	"context"
	"fmt"

	"github.com/saumya712/ledgerlite/internal/model"
)

type accountRepo struct{ db dbtx }

// LockForUpdate locks the given accounts until the transaction ends.
//
// ORDER BY id makes every transaction lock rows in the same order, so
// Alice->Bob and Bob->Alice can never deadlock each other.
// FOR UPDATE makes a second transaction wait until the first commits, then it
// re-reads the fresh balance. That is what stops two transfers from both
// spending the same money.
//
// Note: ids must be lowercase canonical UUID strings, because the returned
// map is keyed by the id as stored in the database. The handler will validate this.
func (r *accountRepo) LockForUpdate(ctx context.Context, ids ...string) (map[string]model.Account, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id::text, name, kind, currency, balance, created_at
		FROM accounts
		WHERE id = ANY($1::text[]::uuid[])
		ORDER BY id
		FOR UPDATE`, ids)
	if err != nil {
		return nil, fmt.Errorf("lock accounts: %w", err)
	}
	defer rows.Close()

	out := make(map[string]model.Account, len(ids))
	for rows.Next() {
		var a model.Account
		var kind string
		if err := rows.Scan(&a.ID, &a.Name, &kind, &a.Currency, &a.Balance, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan account: %w", err)
		}
		a.Kind = model.AccountKind(kind)
		out[a.ID] = a
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read accounts: %w", err)
	}

	// Contract: every requested id must exist.
	for _, id := range ids {
		if _, ok := out[id]; !ok {
			return nil, model.ErrAccountNotFound
		}
	}
	return out, nil
}

// AdjustBalance adds delta (positive or negative) to an account's balance.
// The balance_non_negative CHECK in the database is the last line of defence.
func (r *accountRepo) AdjustBalance(ctx context.Context, id string, delta int64) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE accounts SET balance = balance + $2 WHERE id = $1::text::uuid`, id, delta)
	if err != nil {
		return fmt.Errorf("adjust balance: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return model.ErrAccountNotFound
	}
	return nil
}
