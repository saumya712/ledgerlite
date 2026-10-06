// Package model holds plain data types and errors shared by every layer.
// It imports nothing from this project, so any layer can use it safely.
package model

import (
	"errors"
	"time"
)

// Errors are values, not strings. The service returns them, and the handler
// later maps each one to an HTTP status (for example ErrInsufficientFunds -> 422).
var (
	ErrInvalidAmount         = errors.New("amount must be positive")
	ErrSameAccount           = errors.New("cannot transfer to the same account")
	ErrAccountNotFound       = errors.New("account not found")
	ErrInsufficientFunds     = errors.New("insufficient funds")
	ErrMissingIdempotencyKey = errors.New("idempotency key is required")
	ErrIdempotencyMismatch   = errors.New("idempotency key reused with a different request")
)

type AccountKind string

const (
	KindUser     AccountKind = "user"
	KindExternal AccountKind = "external"
)

// Account mirrors the accounts table.
// Balance is an integer in paise (50000 = Rs 500.00). Never use floats for money.
type Account struct {
	ID        string
	Name      string
	Kind      AccountKind
	Currency  string
	Balance   int64
	CreatedAt time.Time
}

// Transfer mirrors the transfers table: one money movement.
type Transfer struct {
	ID          string
	FromAccount string
	ToAccount   string
	Amount      int64
	CreatedAt   time.Time
}

// Entry mirrors the entries table: one line of the double-entry book.
// Negative amount = debit, positive amount = credit.
type Entry struct {
	ID         int64
	TransferID string
	AccountID  string
	Amount     int64
	CreatedAt  time.Time
}
