package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/saumya712/ledgerlite/internal/model"
)

// TransferCommand is what the service needs to move money. It is NOT the HTTP
// request type: the handler converts JSON into this, so the service never
// depends on HTTP.
type TransferCommand struct {
	ClientID       string
	IdempotencyKey string
	From           string // account id
	To             string // account id
	Amount         int64  // in paise
}

// TransferResult is what the service returns to the handler.
type TransferResult struct {
	TransferID string
	Replayed   bool // true if this was a retry served from the stored result
}

type transferCompletedEvent struct {
	TransferID string `json:"transfer_id"`
	From       string `json:"from"`
	To         string `json:"to"`
	Amount     int64  `json:"amount"`
}

// Service holds the business logic. It depends only on the interfaces in
// ports.go, never on pgx, net/http, or the repository package.
type Service struct {
	txm        TxManager
	externalID string // id of the special 'external' account, used for deposits
}

func NewService(txm TxManager, externalAccountID string) *Service {
	return &Service{txm: txm, externalID: externalAccountID}
}

type storedResponse struct {
	TransferID string `json:"transfer_id"`
}

// Transfer moves money from one account to another, exactly once per
// idempotency key.
//
// TODO: you implement this. Steps, all inside s.txm.WithinTx(ctx, func(...) error {...}):
//
//  0. Before the transaction: if cmd.IdempotencyKey is empty,
//     return model.ErrMissingIdempotencyKey.
//
//  1. Validate (no database needed):
//     amount must be > 0            -> model.ErrInvalidAmount
//     From must differ from To      -> model.ErrSameAccount
//
//  2. Make a requestHash: a sha256 of From + To + Amount (NOT the key).
//     Hint: crypto/sha256, fmt.Sprintf, hex.EncodeToString.
//
//  3. tx.Idempotency().Claim(ctx, clientID, key, requestHash)
//     claim.Replay == true  -> set the result from the stored response,
//     return nil (move no money)
//     error                 -> return it (includes ErrIdempotencyMismatch)
//
//  4. tx.Accounts().LockForUpdate(ctx, cmd.From, cmd.To)
//     -> a map of id -> model.Account with current balances
//
//  5. Check funds: if the From account is a user account and
//     (The external account may go negative: that is how deposits work.)
//
//  6. tx.Transfers().InsertTransfer(...), then InsertEntries with two entries:
//     From account: -Amount        To account: +Amount
//
//  7. tx.Accounts().AdjustBalance for both accounts (-Amount and +Amount)
//
//  8. tx.Outbox().Add(ctx, transferID, "transfer.completed", payload)
//     payload is JSON bytes, e.g. from json.Marshal of a small struct.
//
//  9. tx.Idempotency().Complete(ctx, clientID, key, transferID, response)
//
// If the function inside WithinTx returns any error, the whole transaction
// rolls back and nothing is saved.
func (s *Service) transfer(ctx context.Context, cmd TransferCommand) (TransferResult, error) {
	if cmd.IdempotencyKey == "" {
		return TransferResult{}, model.ErrMissingIdempotencyKey
	}
	if cmd.Amount <= 0 {
		return TransferResult{}, model.ErrInvalidAmount
	}
	if cmd.From == cmd.To {
		return TransferResult{}, model.ErrSameAccount
	}

	requesthash := hashRequest(cmd)
	var result TransferResult
	err := s.txm.WithinTx(ctx, func(ctx context.Context, tx Tx) error {

		claim, err := tx.Idempotency().Claim(ctx, cmd.ClientID, cmd.IdempotencyKey, requesthash)
		if err != nil {
			return err
		}
		if claim.Replay {
			var stored storedResponse
			if err := json.Unmarshal(claim.Response, &stored); err != nil {
				return err
			}
			result = TransferResult{TransferID: stored.TransferID, Replayed: true}
			return nil
		}

		Accounts, err := tx.Accounts().LockForUpdate(ctx, cmd.From, cmd.To)
		if err != nil {
			return err
		}

		fromacc := Accounts[cmd.From]
		if fromacc.Kind == model.KindUser && fromacc.Balance < cmd.Amount {
			return model.ErrInsufficientFunds
		}

		transferid, err := tx.Transfers().InsertTransfer(ctx, model.Transfer{FromAccount: cmd.From, ToAccount: cmd.To, Amount: cmd.Amount})
		if err != nil {
			return err
		}

		entries := []model.Entry{
			{AccountID: cmd.From, Amount: -cmd.Amount},
			{AccountID: cmd.To, Amount: cmd.Amount},
		}

		if err := tx.Transfers().InsertEntries(ctx, transferid, entries); err != nil {
			return err
		}

		if err := tx.Accounts().AdjustBalance(ctx, cmd.From, -cmd.Amount); err != nil {
			return err
		}

		if err := tx.Accounts().AdjustBalance(ctx, cmd.To, cmd.Amount); err != nil {
			return err
		}

		payload, err := json.Marshal(transferCompletedEvent{
			TransferID: transferid,
			From:       cmd.From,
			To:         cmd.To,
			Amount:     cmd.Amount,
		})
		if err != nil {
			return err
		}

		if err := tx.Outbox().Add(ctx, transferid, "transfer.completed", payload); err != nil {
			return err
		}

		response, err := json.Marshal(storedResponse{TransferID: transferid})
		if err != nil {
			return err
		}
		if err := tx.Idempotency().Complete(ctx, cmd.ClientID, cmd.IdempotencyKey, transferid, response); err != nil {
			return err
		}
		result = TransferResult{TransferID: transferid}
		return nil
	})
	if err != nil {
		return TransferResult{}, err
	}
	return result, nil
}

func hashRequest(cmd TransferCommand) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d", cmd.From, cmd.To, cmd.Amount)))
	return hex.EncodeToString(sum[:])
}

// Deposit adds money to an account. A deposit is just a transfer out of the
// special 'external' account, so we reuse Transfer and the books still balance.
func (s *Service) Deposit(ctx context.Context, cmd TransferCommand) (TransferResult, error) {
	cmd.From = s.externalID
	return s.transfer(ctx, cmd)
}

// Transfer is the public entry point. It refuses the special 'external'
// account, otherwise a client could mint money by transferring out of it.
// Only Deposit is allowed to use that account.
func (s *Service) Transfer(ctx context.Context, cmd TransferCommand) (TransferResult, error) {
	if cmd.From == s.externalID || cmd.To == s.externalID {
		return TransferResult{}, model.ErrExternalAccount
	}
	return s.transfer(ctx, cmd)
}
