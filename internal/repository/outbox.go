package repository

import (
	"context"
	"fmt"
)

type outboxRepo struct{ db dbtx }

// Add writes an event in the same transaction as the money movement, so the
// event exists if and only if the transfer committed.
func (r *outboxRepo) Add(ctx context.Context, aggregateID, eventType string, payload []byte) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO outbox (aggregate_id, type, payload)
		VALUES ($1::text::uuid, $2, $3::jsonb)`,
		aggregateID, eventType, payload)
	if err != nil {
		return fmt.Errorf("add outbox event: %w", err)
	}
	return nil
}
