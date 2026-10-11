// Package handler is the HTTP layer. It translates JSON and headers into
// service calls and service errors into status codes. It contains no business
// rules and no SQL.
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
// *service.Service, *service.AccountService and *metrics.Metrics.
// Handler tests use fakes.

type Transfers interface {
	Transfer(ctx context.Context, cmd service.TransferCommand) (service.TransferResult, error)
	Deposit(ctx context.Context, cmd service.TransferCommand) (service.TransferResult, error)
}

type Accounts interface {
	Create(ctx context.Context, name string) (model.Account, error)
	Get(ctx context.Context, id string) (model.Account, error)
}

// Recorder is what the handler needs to report metrics. The handler does not
// know Prometheus exists.
type Recorder interface {
	ObserveRequest(route string, status int, d time.Duration)
	InFlight(delta int)
	TransferOutcome(kind, outcome string)
}

type nopRecorder struct{}

func (nopRecorder) ObserveRequest(string, int, time.Duration) {}
func (nopRecorder) InFlight(int)                              {}
func (nopRecorder) TransferOutcome(string, string)            {}

type Handler struct {
	transfers Transfers
	accounts  Accounts
	log       *slog.Logger
	rec       Recorder
}

// New builds a handler that records no metrics. Call WithRecorder to add them.
func New(transfers Transfers, accounts Accounts, log *slog.Logger) *Handler {
	return &Handler{transfers: transfers, accounts: accounts, log: log, rec: nopRecorder{}}
}

// WithRecorder turns metrics on.
func (h *Handler) WithRecorder(r Recorder) *Handler {
	h.rec = r
	return h
}

// Routes returns the full HTTP handler with request logging applied.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	route := func(pattern string, fn http.HandlerFunc) {
		mux.Handle(pattern, h.instrument(pattern, fn))
	}
	route("GET /healthz", h.health)
	route("POST /v1/accounts", h.createAccount)
	route("GET /v1/accounts/{id}", h.getAccount)
	route("POST /v1/deposits", h.createDeposit)
	route("POST /v1/transfers", h.createTransfer)
	return h.logRequests(mux)
}

// statusRecorder remembers the status code so the logger and metrics can report it.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// instrument records latency, status and in-flight count for one route.
// It labels by the route PATTERN, not the URL, so account ids in the path
// can't create unbounded metric labels.
func (h *Handler) instrument(pattern string, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		h.rec.InFlight(1)
		defer h.rec.InFlight(-1)

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next(rec, r)
		h.rec.ObserveRequest(pattern, rec.status, time.Since(start))
	})
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
