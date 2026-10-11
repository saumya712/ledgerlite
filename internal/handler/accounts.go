package handler

import "net/http"

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// POST /v1/accounts   {"name": "alice"}
func (h *Handler) createAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	acct, err := h.accounts.Create(r.Context(), req.Name)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toAccountResponse(acct))
}

// GET /v1/accounts/{id}
func (h *Handler) getAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := normalizeID(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_account_id", "account id must be a UUID")
		return
	}
	acct, err := h.accounts.Get(r.Context(), id)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAccountResponse(acct))
}
