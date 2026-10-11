package test

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saumya712/ledgerlite/internal/handler"
	"github.com/saumya712/ledgerlite/internal/repository"
	"github.com/saumya712/ledgerlite/internal/service"
)

// apiServer runs the REAL stack (Postgres repository -> service -> handler)
// behind a test HTTP server, the same wiring as cmd/server/main.go.
func apiServer(t *testing.T) (*httptest.Server, *env) {
	t.Helper()
	e := setup(t)
	accounts := service.NewAccountService(repository.NewAccountStore(pool))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(handler.New(e.svc, accounts, log).Routes())
	t.Cleanup(srv.Close)
	return srv, e
}

type apiResp struct {
	status  int
	header  http.Header
	body    map[string]any
	rawBody string
}

func call(t *testing.T, srv *httptest.Server, method, path, body string, headers map[string]string) apiResp {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	must(t, err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	must(t, err)
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	must(t, err)
	var parsed map[string]any
	_ = json.Unmarshal(raw, &parsed)
	return apiResp{status: resp.StatusCode, header: resp.Header, body: parsed, rawBody: string(raw)}
}

func errCode(r apiResp) string {
	e, _ := r.body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func idem(key string) map[string]string { return map[string]string{"Idempotency-Key": key} }

// One walk through the whole API against the real database.
func TestAPI_EndToEnd(t *testing.T) {
	srv, e := apiServer(t)

	// 1. create two accounts
	a := call(t, srv, "POST", "/v1/accounts", `{"name":"alice"}`, nil)
	b := call(t, srv, "POST", "/v1/accounts", `{"name":"bob"}`, nil)
	if a.status != 201 || b.status != 201 {
		t.Fatalf("create accounts: %d / %d (%s)", a.status, b.status, a.rawBody)
	}
	alice, bob := a.body["id"].(string), b.body["id"].(string)

	// 2. deposit Rs 100.00 to alice
	dep := call(t, srv, "POST", "/v1/deposits", fmt.Sprintf(`{"to":%q,"amount":10000}`, alice), idem("d1"))
	if dep.status != 201 {
		t.Fatalf("deposit: %d (%s)", dep.status, dep.rawBody)
	}

	// 3. the same deposit retried: 200, flagged as a replay, same transfer id, no extra money
	again := call(t, srv, "POST", "/v1/deposits", fmt.Sprintf(`{"to":%q,"amount":10000}`, alice), idem("d1"))
	if again.status != 200 || again.header.Get("Idempotent-Replayed") != "true" {
		t.Errorf("retry: status %d, replay header %q", again.status, again.header.Get("Idempotent-Replayed"))
	}
	if again.body["transfer_id"] != dep.body["transfer_id"] {
		t.Error("a replay must return the original transfer id")
	}

	// 4. transfer Rs 30.00 from alice to bob
	tr := call(t, srv, "POST", "/v1/transfers",
		fmt.Sprintf(`{"from":%q,"to":%q,"amount":3000}`, alice, bob), idem("t1"))
	if tr.status != 201 {
		t.Fatalf("transfer: %d (%s)", tr.status, tr.rawBody)
	}

	// 5. balances reflect exactly one deposit and one transfer
	ga := call(t, srv, "GET", "/v1/accounts/"+alice, "", nil)
	gb := call(t, srv, "GET", "/v1/accounts/"+bob, "", nil)
	if ga.body["balance"] != float64(7000) || gb.body["balance"] != float64(3000) {
		t.Errorf("balances: alice=%v bob=%v, want 7000 and 3000", ga.body["balance"], gb.body["balance"])
	}

	// 6. error paths
	tooMuch := call(t, srv, "POST", "/v1/transfers",
		fmt.Sprintf(`{"from":%q,"to":%q,"amount":999999}`, alice, bob), idem("t2"))
	if tooMuch.status != 422 || errCode(tooMuch) != "insufficient_funds" {
		t.Errorf("insufficient funds: %d %s", tooMuch.status, tooMuch.rawBody)
	}

	reused := call(t, srv, "POST", "/v1/transfers",
		fmt.Sprintf(`{"from":%q,"to":%q,"amount":5000}`, alice, bob), idem("t1")) // key t1, different amount
	if reused.status != 422 || errCode(reused) != "idempotency_key_reused" {
		t.Errorf("key reuse: %d %s", reused.status, reused.rawBody)
	}

	noKey := call(t, srv, "POST", "/v1/transfers",
		fmt.Sprintf(`{"from":%q,"to":%q,"amount":100}`, alice, bob), nil)
	if noKey.status != 400 || errCode(noKey) != "missing_idempotency_key" {
		t.Errorf("missing key: %d %s", noKey.status, noKey.rawBody)
	}

	// money cannot be minted through the public API
	mint := call(t, srv, "POST", "/v1/transfers",
		fmt.Sprintf(`{"from":%q,"to":%q,"amount":100000}`, e.externalID, alice), idem("t3"))
	if mint.status != 403 || errCode(mint) != "forbidden_account" {
		t.Errorf("transfer from external: %d %s", mint.status, mint.rawBody)
	}

	missing := call(t, srv, "GET", "/v1/accounts/00000000-0000-7000-8000-000000000000", "", nil)
	if missing.status != 404 || errCode(missing) != "account_not_found" {
		t.Errorf("unknown account: %d %s", missing.status, missing.rawBody)
	}

	if h := call(t, srv, "GET", "/healthz", "", nil); h.status != 200 {
		t.Errorf("healthz: %d", h.status)
	}

	// 7. after all of that, the books still balance
	assertBalance(t, alice, 7000)
	assertBalance(t, bob, 3000)
	assertInvariants(t)
}
