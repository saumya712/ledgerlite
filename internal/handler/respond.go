package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/saumya712/ledgerlite/internal/model"
)

// ---- JSON shapes ----

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type accountResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Currency  string    `json:"currency"`
	Balance   int64     `json:"balance"` // in paise
	CreatedAt time.Time `json:"created_at"`
}

func toAccountResponse(a model.Account) accountResponse {
	return accountResponse{ID: a.ID, Name: a.Name, Currency: a.Currency, Balance: a.Balance, CreatedAt: a.CreatedAt}
}

type transferResponse struct {
	TransferID string `json:"transfer_id"`
	Replayed   bool   `json:"replayed"`
}

// ---- writing responses ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message}})
}

// errorMap is the one place that decides which service error becomes which
// HTTP status. To support a new error, add a row.
var errorMap = []struct {
	err    error
	status int
	code   string
}{
	{model.ErrInvalidAmount, http.StatusBadRequest, "invalid_amount"},
	{model.ErrSameAccount, http.StatusBadRequest, "same_account"},
	{model.ErrMissingIdempotencyKey, http.StatusBadRequest, "missing_idempotency_key"},
	{model.ErrInvalidName, http.StatusBadRequest, "invalid_name"},
	{model.ErrExternalAccount, http.StatusForbidden, "forbidden_account"},
	{model.ErrAccountNotFound, http.StatusNotFound, "account_not_found"},
	{model.ErrInsufficientFunds, http.StatusUnprocessableEntity, "insufficient_funds"},
	{model.ErrIdempotencyMismatch, http.StatusUnprocessableEntity, "idempotency_key_reused"},
}

// writeServiceError maps known errors to their status. Anything unknown is
// logged in full but returned to the client as a generic 500, so internal
// details (SQL errors, stack information) never leak.
func (h *Handler) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	for _, m := range errorMap {
		if errors.Is(err, m.err) {
			writeError(w, m.status, m.code, m.err.Error())
			return
		}
	}
	h.log.Error("unhandled error", "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
}

// ---- reading requests ----

// decodeJSON reads exactly one JSON object, rejects unknown fields and
// oversized bodies. It writes the 400 itself and returns false on failure.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MiB
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body is not valid JSON for this endpoint")
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain exactly one JSON object")
		return false
	}
	return true
}

var uuidPattern = regexp.MustCompile(
	`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// normalizeID validates a UUID and lowercases it. The repository compares ids
// as stored in Postgres (lowercase), so ids must be normalized here.
func normalizeID(s string) (string, bool) {
	if !uuidPattern.MatchString(s) {
		return "", false
	}
	return strings.ToLower(s), true
}

// requestMeta reads the client id and idempotency key from headers.
// X-Client-ID scopes idempotency keys per client; until we add authentication
// it is self-declared, and it defaults to "default".
func requestMeta(w http.ResponseWriter, r *http.Request) (clientID, key string, ok bool) {
	clientID = strings.TrimSpace(r.Header.Get("X-Client-ID"))
	if clientID == "" {
		clientID = "default"
	}
	key = strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(clientID) > 100 || len(key) > 255 {
		writeError(w, http.StatusBadRequest, "invalid_header", "X-Client-ID or Idempotency-Key is too long")
		return "", "", false
	}
	return clientID, key, true
}

// outcomeOf names the result of a failed transfer for metrics. It uses the
// same error codes the API returns, so the set of values stays small and fixed.
func outcomeOf(err error) string {
	for _, m := range errorMap {
		if errors.Is(err, m.err) {
			return m.code
		}
	}
	return "internal_error"
}
