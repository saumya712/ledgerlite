package repository

import (
	"context"
	"fmt"

	"github.com/saumya712/ledgerlite/internal/model"
)

type transferRepo struct{ db dbtx }

// InsertTransfer records one money movement and returns its generated id.
func (r *transferRepo) InsertTransfer(ctx context.Context, t model.Transfer) (string, error) {
	var id string
	err := r.db.QueryRow(ctx, `
		INSERT INTO transfers (from_account, to_account, amount)
		VALUES ($1::text::uuid, $2::text::uuid, $3)
		RETURNING id::text`,
		t.FromAccount, t.ToAccount, t.Amount).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("insert transfer: %w", err)
	}
	return id, nil
}

// InsertEntries appends the double-entry lines. Rows are never updated or
// deleted afterwards (database triggers forbid it).
func (r *transferRepo) InsertEntries(ctx context.Context, transferID string, entries []model.Entry) error {
	for _, e := range entries {
		_, err := r.db.Exec(ctx, `
			INSERT INTO entries (transfer_id, account_id, amount)
			VALUES ($1::text::uuid, $2::text::uuid, $3)`,
			transferID, e.AccountID, e.Amount)
		if err != nil {
			return fmt.Errorf("insert entry: %w", err)
		}
	}
	return nil
}
