package test

import (
	"errors"
	"testing"

	"github.com/saumya712/ledgerlite/internal/model"
	"github.com/saumya712/ledgerlite/internal/repository"
)

func TestAccountStore_CreateAndGet(t *testing.T) {
	setup(t)
	store := repository.NewAccountStore(pool)
	ctx := bg(t)

	created, err := store.Create(ctx, "alice")
	must(t, err)
	if created.ID == "" || created.Name != "alice" || created.Balance != 0 ||
		created.Currency != "INR" || created.Kind != model.KindUser {
		t.Errorf("unexpected new account: %+v", created)
	}

	got, err := store.Get(ctx, created.ID)
	must(t, err)
	if got.ID != created.ID || got.Name != "alice" {
		t.Errorf("Get returned %+v, want %+v", got, created)
	}
}

func TestAccountStore_GetUnknownID(t *testing.T) {
	setup(t)
	store := repository.NewAccountStore(pool)

	_, err := store.Get(bg(t), "00000000-0000-7000-8000-000000000000")
	if !errors.Is(err, model.ErrAccountNotFound) {
		t.Fatalf("err = %v, want ErrAccountNotFound", err)
	}
}

func TestAccountStore_ExternalAccountID(t *testing.T) {
	e := setup(t)
	store := repository.NewAccountStore(pool)

	id, err := store.ExternalAccountID(bg(t))
	must(t, err)
	if id != e.externalID {
		t.Errorf("ExternalAccountID = %q, want %q", id, e.externalID)
	}
}

// If the migrations were never applied the server must refuse to start,
// not run with a missing external account.
func TestAccountStore_ExternalAccountMissingIsAnError(t *testing.T) {
	setup(t)
	ctx := bg(t)
	_, err := pool.Exec(ctx,
		`TRUNCATE entries, transfers, idempotency_keys, outbox, accounts RESTART IDENTITY`)
	must(t, err)

	store := repository.NewAccountStore(pool)
	if _, err := store.ExternalAccountID(ctx); err == nil {
		t.Error("expected an error when no external account exists")
	}
}
