package service

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/saumya712/ledgerlite/internal/model"
)

// AccountStore is what AccountService needs from storage. It is separate from
// the transactional ports in ports.go: creating and reading an account does
// not need a multi-step transaction.
type AccountStore interface {
	Create(ctx context.Context, name string) (model.Account, error)
	Get(ctx context.Context, id string) (model.Account, error)
}

// AccountService handles account creation and lookup. It is a separate type
// from Service so each one has a single, small responsibility.
type AccountService struct {
	store AccountStore
}

func NewAccountService(store AccountStore) *AccountService {
	return &AccountService{store: store}
}

func (s *AccountService) Create(ctx context.Context, name string) (model.Account, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 100 {
		return model.Account{}, model.ErrInvalidName
	}
	return s.store.Create(ctx, name)
}

func (s *AccountService) Get(ctx context.Context, id string) (model.Account, error) {
	return s.store.Get(ctx, id)
}
