package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/saumya712/ledgerlite/internal/model"
	"github.com/saumya712/ledgerlite/internal/service"
)

// These interfaces are defined here, by the consumer, and are satisfied by
// *service.Service and *service.AccountService. Handler tests use fakes.

type Transfers interface {
	Transfer(ctx context.Context, cmd service.TransferCommand) (service.TransferResult, error)
	Deposit(ctx context.Context, cmd service.TransferCommand) (service.TransferResult, error)
}

type Accounts interface {
	Create(ctx context.Context, name string) (model.Account, error)
	Get(ctx context.Context, id string) (model.Account, error)
}

type Handler struct {
	transfers Transfers
	accounts  Accounts
	log       *slog.Logger
}

func New(transfers Transfers, accounts Accounts, log *slog.Logger) *Handler {
	return &Handler{transfers: transfers, accounts: accounts, log: log}
}

// Routes returns the full HTTP handler with request logging applied.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.health)
	mux.HandleFunc("POST /v1/accounts", h.createAccount)
	mux.HandleFunc("GET /v1/accounts/{id}", h.getAccount)
	mux.HandleFunc("POST /v1/deposits", h.createDeposit)
	mux.HandleFunc("POST /v1/transfers", h.createTransfer)
	return h.logRequests(mux)
}

// statusRecorder remembers the status code so the logger can report it.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (h *Handler) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		h.log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds())
	})
}
