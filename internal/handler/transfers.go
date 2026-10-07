package handler
 
import (
	"net/http"
 
	"github.com/saumya712/ledgerlite/internal/service"
)
 
type transferRequest struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Amount int64  `json:"amount"` // in paise
}
 
type depositRequest struct {
	To     string `json:"to"`
	Amount int64  `json:"amount"` // in paise
}
 
// POST /v1/transfers   (header: Idempotency-Key)
func (h *Handler) createTransfer(w http.ResponseWriter, r *http.Request) {
	var req transferRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	from, ok := normalizeID(req.From)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_account_id", "from must be a valid account id (UUID)")
		return
	}
	to, ok := normalizeID(req.To)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_account_id", "to must be a valid account id (UUID)")
		return
	}
	clientID, key, ok := requestMeta(w, r)
	if !ok {
		return
	}
 
	res, err := h.transfers.Transfer(r.Context(), service.TransferCommand{
		ClientID: clientID, IdempotencyKey: key, From: from, To: to, Amount: req.Amount,
	})
	h.respondTransfer(w, r, res, err)
}
 
// POST /v1/deposits   (header: Idempotency-Key)
func (h *Handler) createDeposit(w http.ResponseWriter, r *http.Request) {
	var req depositRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	to, ok := normalizeID(req.To)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_account_id", "to must be a valid account id (UUID)")
		return
	}
	clientID, key, ok := requestMeta(w, r)
	if !ok {
		return
	}
 
	res, err := h.transfers.Deposit(r.Context(), service.TransferCommand{
		ClientID: clientID, IdempotencyKey: key, To: to, Amount: req.Amount,
	})
	h.respondTransfer(w, r, res, err)
}
 
// respondTransfer: 201 for a newly executed request, 200 for a replay of an
// earlier identical request (marked with an Idempotent-Replayed header).
func (h *Handler) respondTransfer(w http.ResponseWriter, r *http.Request, res service.TransferResult, err error) {
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	status := http.StatusCreated
	if res.Replayed {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeJSON(w, status, transferResponse{TransferID: res.TransferID, Replayed: res.Replayed})
}