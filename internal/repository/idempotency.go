package repository

import (
	"context"
	"fmt"

	"github.com/saumya712/ledgerlite/internal/model"
	"github.com/saumya712/ledgerlite/internal/service"
)

type idempotencyRepo struct{ db dbtx }

// Claim tries to insert the key.
//
// If another transaction is inserting the same key right now, this INSERT
// WAITS until that transaction finishes. If it committed, we get a conflict
// (0 rows inserted) and read its result. If it rolled back, our insert succeeds.
// That wait is what makes two simultaneous identical requests safe.
func (r *idempotencyRepo) Claim(ctx context.Context, clientID, key, requestHash string) (service.IdempotencyClaim, error) {
	tag, err := r.db.Exec(ctx, `
		INSERT INTO idempotency_keys (client_id, key, request_hash)
		VALUES ($1, $2, $3)
		ON CONFLICT (client_id, key) DO NOTHING`,
		clientID, key, requestHash)
	if err != nil {
		return service.IdempotencyClaim{}, fmt.Errorf("claim idempotency key: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return service.IdempotencyClaim{}, nil // new key: go ahead
	}

	// The key already exists and is committed: read what is stored.
	var storedHash string
	var response []byte
	err = r.db.QueryRow(ctx, `
		SELECT request_hash, response_body
		FROM idempotency_keys
		WHERE client_id = $1 AND key = $2`,
		clientID, key).Scan(&storedHash, &response)
	if err != nil {
		return service.IdempotencyClaim{}, fmt.Errorf("read idempotency key: %w", err)
	}
	if storedHash != requestHash {
		return service.IdempotencyClaim{}, model.ErrIdempotencyMismatch
	}
	return service.IdempotencyClaim{Replay: true, Response: response}, nil
}

// Complete stores the response next to the key so a retry gets the same answer.
func (r *idempotencyRepo) Complete(ctx context.Context, clientID, key, transferID string, response []byte) error {
	_, err := r.db.Exec(ctx, `
		UPDATE idempotency_keys
		SET transfer_id = $3::text::uuid, response_status = 201, response_body = $4::jsonb
		WHERE client_id = $1 AND key = $2`,
		clientID, key, transferID, response)
	if err != nil {
		return fmt.Errorf("complete idempotency key: %w", err)
	}
	return nil
}
