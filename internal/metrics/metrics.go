// Package metrics exposes Prometheus metrics. It implements the small
// Recorder interface that the handler defines, so neither the handler nor the
// service imports Prometheus.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	registry  *prometheus.Registry
	requests  *prometheus.CounterVec
	duration  *prometheus.HistogramVec
	inFlight  prometheus.Gauge
	transfers *prometheus.CounterVec
}

// New creates a private registry (no global state) with Go runtime and
// process metrics already included.
func New() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ledgerlite_http_requests_total",
			Help: "HTTP requests by route and status code.",
		}, []string{"route", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "ledgerlite_http_request_duration_seconds",
			Help:    "HTTP request latency by route.",
			Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
		}, []string{"route"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ledgerlite_http_in_flight_requests",
			Help: "Requests currently being served.",
		}),
		transfers: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ledgerlite_transfers_total",
			Help: "Transfer and deposit attempts by outcome (created, replayed, or an error code).",
		}, []string{"kind", "outcome"}),
	}
	m.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.requests, m.duration, m.inFlight, m.transfers,
	)
	return m
}

// ---- implements handler.Recorder ----

// ObserveRequest records one finished request. route is the registered route
// pattern ("GET /v1/accounts/{id}"), never the raw URL, so account ids can't
// create unbounded label values.
func (m *Metrics) ObserveRequest(route string, status int, d time.Duration) {
	m.requests.WithLabelValues(route, strconv.Itoa(status)).Inc()
	m.duration.WithLabelValues(route).Observe(d.Seconds())
}

func (m *Metrics) InFlight(delta int) { m.inFlight.Add(float64(delta)) }

func (m *Metrics) TransferOutcome(kind, outcome string) {
	m.transfers.WithLabelValues(kind, outcome).Inc()
}

// ---- database pool ----

// PoolStatter is satisfied by *pgxpool.Pool.
type PoolStatter interface {
	Stat() *pgxpool.Stat
}

// RegisterPool exposes connection-pool saturation. If "in use" sits at the
// maximum and "empty acquires" keep climbing after startup, requests are
// queuing for a connection: the pool, not Postgres, is the bottleneck.
func (m *Metrics) RegisterPool(p PoolStatter) {
	m.registry.MustRegister(
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "ledgerlite_db_pool_acquired_conns",
			Help: "Connections currently in use.",
		}, func() float64 { return float64(p.Stat().AcquiredConns()) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "ledgerlite_db_pool_idle_conns",
			Help: "Connections open and idle.",
		}, func() float64 { return float64(p.Stat().IdleConns()) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "ledgerlite_db_pool_max_conns",
			Help: "Maximum pool size.",
		}, func() float64 { return float64(p.Stat().MaxConns()) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "ledgerlite_db_pool_acquires_total",
			Help: "Connection acquisitions.",
		}, func() float64 { return float64(p.Stat().AcquireCount()) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "ledgerlite_db_pool_empty_acquires_total",
			Help: "Acquisitions that found no idle connection and had to wait for one to be released or created.",
		}, func() float64 { return float64(p.Stat().EmptyAcquireCount()) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "ledgerlite_db_pool_acquire_seconds_total",
			Help: "Total time spent acquiring connections (mostly waiting when the pool is saturated).",
		}, func() float64 { return p.Stat().AcquireDuration().Seconds() }),
	)
}

// Handler serves the metrics in Prometheus text format (GET /metrics).
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}
