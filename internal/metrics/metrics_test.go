package metrics

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestMetricsAreExposed(t *testing.T) {
	m := New()
	m.ObserveRequest("POST /v1/transfers", 201, 12*time.Millisecond)
	m.ObserveRequest("POST /v1/transfers", 201, 8*time.Millisecond)
	m.ObserveRequest("POST /v1/transfers", 422, 3*time.Millisecond)
	m.TransferOutcome("transfer", "created")
	m.TransferOutcome("transfer", "insufficient_funds")
	m.InFlight(1)

	out := scrape(t, m)
	for _, want := range []string{
		`ledgerlite_http_requests_total{route="POST /v1/transfers",status="201"} 2`,
		`ledgerlite_http_requests_total{route="POST /v1/transfers",status="422"} 1`,
		`ledgerlite_http_request_duration_seconds_count{route="POST /v1/transfers"} 3`,
		`ledgerlite_transfers_total{kind="transfer",outcome="created"} 1`,
		`ledgerlite_transfers_total{kind="transfer",outcome="insufficient_funds"} 1`,
		`ledgerlite_http_in_flight_requests 1`,
		`go_goroutines`, // the Go runtime collector is registered
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics output is missing %q", want)
		}
	}
}

func TestInFlightGoesBackDown(t *testing.T) {
	m := New()
	m.InFlight(1)
	m.InFlight(1)
	m.InFlight(-1)
	m.InFlight(-1)
	if out := scrape(t, m); !strings.Contains(out, "ledgerlite_http_in_flight_requests 0") {
		t.Error("in-flight gauge should return to 0")
	}
}
