package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/saumya712/ledgerlite/internal/model"
	"github.com/saumya712/ledgerlite/internal/service"
)

const (
	aliceID = "0197a000-0000-7000-8000-00000000000a"
	bobID   = "0197a000-0000-7000-8000-00000000000b"
)

// ---- fakes: the handler only knows the Transfers and Accounts interfaces ----

type fakeTransfers struct {
	res   service.TransferResult
	err   error
	calls int
	last  service.TransferCommand
}

func (f *fakeTransfers) Transfer(_ context.Context, cmd service.TransferCommand) (service.TransferResult, error) {
	f.calls++
	f.last = cmd
	return f.res, f.err
}

func (f *fakeTransfers) Deposit(_ context.Context, cmd service.TransferCommand) (service.TransferResult, error) {
	f.calls++
	f.last = cmd
	return f.res, f.err
}

type fakeAccounts struct {
	acct     model.Account
	err      error
	calls    int
	lastID   string
	lastName string
}

func (f *fakeAccounts) Create(_ context.Context, name string) (model.Account, error) {
	f.calls++
	f.lastName = name
	return f.acct, f.err
}

func (f *fakeAccounts) Get(_ context.Context, id string) (model.Account, error) {
	f.calls++
	f.lastID = id
	return f.acct, f.err
}

// ---- helpers ----

func newTestHandler(tr Transfers, ac Accounts) http.Handler {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(tr, ac, log).Routes()
}

func do(h http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not an error body: %q", rec.Body.String())
	}
	return body.Error.Code
}

func transferBody(from, to string, amount int64) string {
	return fmt.Sprintf(`{"from":%q,"to":%q,"amount":%d}`, from, to, amount)
}

var withKey = map[string]string{"Idempotency-Key": "k1"}

// ---- tests ----

func TestCreateTransfer_Success(t *testing.T) {
	ft := &fakeTransfers{res: service.TransferResult{TransferID: "t-1"}}
	h := newTestHandler(ft, &fakeAccounts{})

	// Uppercase ids must be normalised to lowercase before reaching the service.
	body := transferBody(strings.ToUpper(aliceID), bobID, 8000)
	rec := do(h, "POST", "/v1/transfers", body, withKey)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	var got transferResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.TransferID != "t-1" || got.Replayed {
		t.Errorf("response = %+v", got)
	}
	want := service.TransferCommand{
		ClientID: "default", IdempotencyKey: "k1", From: aliceID, To: bobID, Amount: 8000,
	}
	if ft.last != want {
		t.Errorf("service got %+v, want %+v", ft.last, want)
	}
}

func TestCreateTransfer_ReplayReturns200(t *testing.T) {
	ft := &fakeTransfers{res: service.TransferResult{TransferID: "t-1", Replayed: true}}
	h := newTestHandler(ft, &fakeAccounts{})

	rec := do(h, "POST", "/v1/transfers", transferBody(aliceID, bobID, 8000), withKey)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Error("missing Idempotent-Replayed header")
	}
}

func TestClientIDHeaderIsPassedThrough(t *testing.T) {
	ft := &fakeTransfers{res: service.TransferResult{TransferID: "t-1"}}
	h := newTestHandler(ft, &fakeAccounts{})

	headers := map[string]string{"Idempotency-Key": "k1", "X-Client-ID": "shop-42"}
	do(h, "POST", "/v1/transfers", transferBody(aliceID, bobID, 100), headers)

	if ft.last.ClientID != "shop-42" {
		t.Errorf("ClientID = %q, want shop-42", ft.last.ClientID)
	}
}

func TestServiceErrorsMapToStatus(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{model.ErrInvalidAmount, 400, "invalid_amount"},
		{model.ErrSameAccount, 400, "same_account"},
		{model.ErrMissingIdempotencyKey, 400, "missing_idempotency_key"},
		{model.ErrExternalAccount, 403, "forbidden_account"},
		{model.ErrAccountNotFound, 404, "account_not_found"},
		{model.ErrInsufficientFunds, 422, "insufficient_funds"},
		{model.ErrIdempotencyMismatch, 422, "idempotency_key_reused"},
		{fmt.Errorf("wrapped: %w", model.ErrInsufficientFunds), 422, "insufficient_funds"},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			h := newTestHandler(&fakeTransfers{err: tc.err}, &fakeAccounts{})
			rec := do(h, "POST", "/v1/transfers", transferBody(aliceID, bobID, 100), withKey)
			if rec.Code != tc.status {
				t.Errorf("status = %d, want %d", rec.Code, tc.status)
			}
			if got := errorCode(t, rec); got != tc.code {
				t.Errorf("code = %q, want %q", got, tc.code)
			}
		})
	}
}

func TestUnknownErrorIsHiddenFromClient(t *testing.T) {
	ft := &fakeTransfers{err: errors.New("boom: pq: password authentication failed")}
	h := newTestHandler(ft, &fakeAccounts{})

	rec := do(h, "POST", "/v1/transfers", transferBody(aliceID, bobID, 100), withKey)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "boom") || strings.Contains(rec.Body.String(), "password") {
		t.Errorf("internal error details leaked to the client: %s", rec.Body.String())
	}
}

func TestBadRequestsNeverReachTheService(t *testing.T) {
	cases := map[string]string{
		"broken json":      `{`,
		"unknown field":    fmt.Sprintf(`{"from":%q,"to":%q,"amount":100,"fee":5}`, aliceID, bobID),
		"amount is string": fmt.Sprintf(`{"from":%q,"to":%q,"amount":"100"}`, aliceID, bobID),
		"amount is float":  fmt.Sprintf(`{"from":%q,"to":%q,"amount":10.5}`, aliceID, bobID),
		"from not a uuid":  fmt.Sprintf(`{"from":"alice","to":%q,"amount":100}`, bobID),
		"to not a uuid":    fmt.Sprintf(`{"from":%q,"to":"bob","amount":100}`, aliceID),
		"two objects":      transferBody(aliceID, bobID, 100) + `{}`,
		"empty body":       ``,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			ft := &fakeTransfers{}
			h := newTestHandler(ft, &fakeAccounts{})
			rec := do(h, "POST", "/v1/transfers", body, withKey)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			if ft.calls != 0 {
				t.Error("the service must not be called for a malformed request")
			}
		})
	}
}

func TestOverlongIdempotencyKeyIsRejected(t *testing.T) {
	ft := &fakeTransfers{}
	h := newTestHandler(ft, &fakeAccounts{})

	headers := map[string]string{"Idempotency-Key": strings.Repeat("x", 300)}
	rec := do(h, "POST", "/v1/transfers", transferBody(aliceID, bobID, 100), headers)

	if rec.Code != http.StatusBadRequest || ft.calls != 0 {
		t.Errorf("status = %d, calls = %d; want 400 and 0 calls", rec.Code, ft.calls)
	}
}

func TestCreateDeposit(t *testing.T) {
	ft := &fakeTransfers{res: service.TransferResult{TransferID: "t-9"}}
	h := newTestHandler(ft, &fakeAccounts{})

	rec := do(h, "POST", "/v1/deposits", fmt.Sprintf(`{"to":%q,"amount":5000}`, aliceID), withKey)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if ft.last.To != aliceID || ft.last.Amount != 5000 || ft.last.From != "" {
		t.Errorf("service got %+v", ft.last)
	}
}

func TestGetAccount(t *testing.T) {
	acct := model.Account{ID: aliceID, Name: "alice", Currency: "INR", Balance: 4200, CreatedAt: time.Unix(0, 0).UTC()}
	fa := &fakeAccounts{acct: acct}
	h := newTestHandler(&fakeTransfers{}, fa)

	rec := do(h, "GET", "/v1/accounts/"+strings.ToUpper(aliceID), "", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got accountResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != aliceID || got.Balance != 4200 || got.Name != "alice" {
		t.Errorf("response = %+v", got)
	}
	if fa.lastID != aliceID {
		t.Errorf("service got id %q, want the lowercase %q", fa.lastID, aliceID)
	}
}

func TestGetAccount_NotFoundAndInvalid(t *testing.T) {
	fa := &fakeAccounts{err: model.ErrAccountNotFound}
	h := newTestHandler(&fakeTransfers{}, fa)

	if rec := do(h, "GET", "/v1/accounts/"+aliceID, "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown account: status = %d, want 404", rec.Code)
	}

	fa.calls = 0
	if rec := do(h, "GET", "/v1/accounts/not-a-uuid", "", nil); rec.Code != http.StatusBadRequest || fa.calls != 0 {
		t.Errorf("invalid id: status = %d, calls = %d; want 400 and 0 calls", rec.Code, fa.calls)
	}
}

func TestCreateAccount(t *testing.T) {
	fa := &fakeAccounts{acct: model.Account{ID: aliceID, Name: "alice", Currency: "INR"}}
	h := newTestHandler(&fakeTransfers{}, fa)

	rec := do(h, "POST", "/v1/accounts", `{"name":"alice"}`, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if fa.lastName != "alice" {
		t.Errorf("service got name %q", fa.lastName)
	}

	fa.err = model.ErrInvalidName
	if rec := do(h, "POST", "/v1/accounts", `{"name":""}`, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid name: status = %d, want 400", rec.Code)
	}
}

func TestHealth(t *testing.T) {
	h := newTestHandler(&fakeTransfers{}, &fakeAccounts{})
	if rec := do(h, "GET", "/healthz", "", nil); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

// ---- metrics recording ----

type fakeRecorder struct {
	outcomes []string // "kind/outcome"
	routes   []string
	statuses []int
	inFlight int
}

func (f *fakeRecorder) ObserveRequest(route string, status int, _ time.Duration) {
	f.routes = append(f.routes, route)
	f.statuses = append(f.statuses, status)
}
func (f *fakeRecorder) InFlight(delta int) { f.inFlight += delta }
func (f *fakeRecorder) TransferOutcome(kind, outcome string) {
	f.outcomes = append(f.outcomes, kind+"/"+outcome)
}

func recordingHandler(tr Transfers, ac Accounts, rec Recorder) http.Handler {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(tr, ac, log).WithRecorder(rec).Routes()
}

func TestRecorder_TransferOutcomes(t *testing.T) {
	cases := []struct {
		name    string
		res     service.TransferResult
		err     error
		outcome string
		status  int
	}{
		{"created", service.TransferResult{TransferID: "t"}, nil, "created", 201},
		{"replayed", service.TransferResult{TransferID: "t", Replayed: true}, nil, "replayed", 200},
		{"insufficient", service.TransferResult{}, model.ErrInsufficientFunds, "insufficient_funds", 422},
		{"unknown error", service.TransferResult{}, errors.New("boom"), "internal_error", 500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &fakeRecorder{}
			h := recordingHandler(&fakeTransfers{res: tc.res, err: tc.err}, &fakeAccounts{}, rec)

			do(h, "POST", "/v1/transfers", transferBody(aliceID, bobID, 100), withKey)

			if len(rec.outcomes) != 1 || rec.outcomes[0] != "transfer/"+tc.outcome {
				t.Errorf("outcomes = %v, want [transfer/%s]", rec.outcomes, tc.outcome)
			}
			if len(rec.statuses) != 1 || rec.statuses[0] != tc.status {
				t.Errorf("statuses = %v, want [%d]", rec.statuses, tc.status)
			}
			if rec.inFlight != 0 {
				t.Errorf("in-flight gauge = %d after the request, want 0", rec.inFlight)
			}
		})
	}
}

func TestRecorder_DepositsAreLabelledAsDeposits(t *testing.T) {
	rec := &fakeRecorder{}
	h := recordingHandler(&fakeTransfers{res: service.TransferResult{TransferID: "t"}}, &fakeAccounts{}, rec)

	do(h, "POST", "/v1/deposits", fmt.Sprintf(`{"to":%q,"amount":100}`, aliceID), withKey)

	if len(rec.outcomes) != 1 || rec.outcomes[0] != "deposit/created" {
		t.Errorf("outcomes = %v, want [deposit/created]", rec.outcomes)
	}
}

// Account ids in the URL must never become metric labels: that would create a
// new time series per account and eventually exhaust Prometheus.
func TestRecorder_RouteLabelIsThePatternNotTheURL(t *testing.T) {
	rec := &fakeRecorder{}
	h := recordingHandler(&fakeTransfers{}, &fakeAccounts{acct: model.Account{ID: aliceID}}, rec)

	do(h, "GET", "/v1/accounts/"+aliceID, "", nil)
	do(h, "GET", "/v1/accounts/"+bobID, "", nil)

	want := "GET /v1/accounts/{id}"
	if len(rec.routes) != 2 || rec.routes[0] != want || rec.routes[1] != want {
		t.Errorf("routes = %v, want two of %q", rec.routes, want)
	}
}
