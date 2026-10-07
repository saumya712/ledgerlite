package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/saumya712/ledgerlite/internal/model"
	"github.com/saumya712/ledgerlite/internal/service"
)

// AccountStore implements service.AccountStore. These operations are single
// statements, so they use the pool directly instead of a transaction.
type AccountStore struct {
	pool *pgxpool.Pool
}

func NewAccountStore(pool *pgxpool.Pool) *AccountStore {
	return &AccountStore{pool: pool}
}

const accountColumns = `id::text, name, kind, currency, balance, created_at`

func scanAccount(row pgx.Row) (model.Account, error) {
	var a model.Account
	var kind string
	if err := row.Scan(&a.ID, &a.Name, &kind, &a.Currency, &a.Balance, &a.CreatedAt); err != nil {
		return model.Account{}, err
	}
	a.Kind = model.AccountKind(kind)
	return a, nil
}

func (s *AccountStore) Create(ctx context.Context, name string) (model.Account, error) {
	a, err := scanAccount(s.pool.QueryRow(ctx,
		`INSERT INTO accounts (name) VALUES ($1) RETURNING `+accountColumns, name))
	if err != nil {
		return model.Account{}, fmt.Errorf("create account: %w", err)
	}
	return a, nil
}

func (s *AccountStore) Get(ctx context.Context, id string) (model.Account, error) {
	a, err := scanAccount(s.pool.QueryRow(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE id = $1::text::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Account{}, model.ErrAccountNotFound
	}
	if err != nil {
		return model.Account{}, fmt.Errorf("get account: %w", err)
	}
	return a, nil
}

var _ service.AccountStore = (*AccountStore)(nil)

// ExternalAccountID returns the id of the special 'external' account (the
// source of deposits). It is read once at startup. Exactly one must exist:
// the migration creates it.
func (s *AccountStore) ExternalAccountID(ctx context.Context) (string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text FROM accounts WHERE kind = 'external'`)
	if err != nil {
		return "", fmt.Errorf("find external account: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", fmt.Errorf("scan external account: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("read external account: %w", err)
	}

	switch len(ids) {
	case 1:
		return ids[0], nil
	case 0:
		return "", errors.New("no external account found: have the migrations been applied?")
	default:
		return "", fmt.Errorf("expected exactly one external account, found %d", len(ids))
	}
}