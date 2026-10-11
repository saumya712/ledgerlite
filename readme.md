# Ledgerlite

A small money-movement service in Go and PostgreSQL that stays correct under concurrency, retries, and crashes.

Moving money looks easy until two requests arrive at the same moment, a client retries after a timeout, or the process dies halfway through a transfer. Ledgerlite is built so none of those can create, destroy, or double-spend money, and the repository proves it with tests that run against a real database.

![alt text](<Screenshot 2026-10-08 212737.png>)

## What it guarantees

| Guarantee | How |
|---|---|
| Money is never created or destroyed | Double-entry bookkeeping: every transfer writes entries that sum to zero, enforced by a database constraint trigger at commit |
| No double-spend | `SELECT ... FOR UPDATE` row locks, so concurrent transfers on one account run one at a time |
| A retried request moves money once | Idempotency keys, claimed inside the same transaction as the transfer |
| No deadlocks between opposite transfers | Accounts are always locked in ascending id order |
| A crash leaves no half-finished transfer | One database transaction per transfer: all of it commits, or none of it |
| History cannot be rewritten by the application | `UPDATE` and `DELETE` on ledger tables are rejected by triggers |

## How a transfer works

`POST /v1/transfers` runs these steps inside **one** transaction:

1. **Claim the idempotency key.** If this key already completed, return the stored result and stop (HTTP 200, `Idempotent-Replayed: true`). If it exists with a *different* request body, reject it.
2. **Lock both accounts** in ascending id order and read their balances.
3. **Check funds** using the locked balances, not a separate read.
4. **Insert the transfer and its two entries** (debit and credit).
5. **Update the stored balances.**
6. **Write an outbox event** in the same transaction.
7. **Save the response** against the idempotency key.

The order matters. The key is claimed before locking so a replay never touches the accounts. Accounts are locked before the funds check so the balance you test is one nobody else can change. If any step fails, the transaction rolls back, including the idempotency key, so the client can retry safely.

## Architecture

```mermaid
flowchart LR
    Client -->|HTTP| Handler
    subgraph ledgerlite
        Handler --> Service
        Repository -.implements ports of.-> Service
        Handler -.-> Metrics["/metrics"]
    end
    Repository --> DB[(PostgreSQL 18)]
    Prometheus --> Metrics
    Grafana --> Prometheus
```

Three layers, each with one job:

- **handler**: HTTP only. Parses JSON and headers, calls the service, maps errors to status codes. No business rules, no SQL.
- **service**: business logic. Defines the interfaces it needs (`AccountRepo`, `TransferRepo`, `IdempotencyRepo`, `OutboxRepo`, `TxManager`).
- **repository**: SQL only. Implements those interfaces with pgx.

Dependencies point at the service: the handler and the repository both depend on it, and it depends on neither. That is what lets the service be unit-tested with in-memory fakes, and the handler with fake services. Metrics reach the handler through a small `Recorder` interface, so neither the handler nor the service imports Prometheus.

```
cmd/server/main.go        wiring, config, graceful shutdown
internal/model/           plain data types and errors
internal/handler/         HTTP layer
internal/service/         business logic and the interfaces it needs
internal/repository/      PostgreSQL implementations
internal/config/          environment variables, validated
internal/metrics/         Prometheus metrics
migrations/001_init.sql   schema, constraints, triggers
test/                     integration tests against real PostgreSQL
loadtest/ledger.js        k6 load test
deploy/                   Prometheus and Grafana configuration
```

## What breaks, and what happens

Each row names the test that checks it.

| Situation | What happens | Test |
|---|---|---|
| 50 requests try to spend ₹30 each from ₹100, all at once | Exactly 3 succeed, 47 get `insufficient_funds`, the balance ends at ₹10 | `TestConcurrentTransfers_NoDoubleSpend` |
| 100 simultaneous transfers from one account | None are lost, final balances are exact | `TestConcurrentTransfers_NoLostUpdates` |
| The same request is sent 50 times at the same instant | Money moves once, every caller gets the same transfer id | `TestIdempotency_SameKeyConcurrent` |
| A key is reused with a different amount | Rejected with 422 `idempotency_key_reused` | `TestIdempotency_SameKeyDifferentBody` |
| Alice→Bob and Bob→Alice run simultaneously | No deadlock, all succeed | `TestOppositeTransfers_NoDeadlock` |
| The process fails after the debit but before the credit | Everything rolls back, no trace is left | `TestRollback_PartialWorkLeavesNothing` |
| A bug writes entries that do not sum to zero | The database rejects the transaction at commit | `TestDatabaseRejectsUnbalancedEntries` |
| Someone runs `UPDATE` or `DELETE` on ledger rows | Rejected by a trigger | `TestLedgerRowsAreImmutable` |
| A client transfers out of the internal `external` account to mint money | 403 `forbidden_account` | `TestAPI_EndToEnd`, service unit tests |
| A request is rejected (insufficient funds) | No rows are written, and the idempotency key stays free for a retry | `TestInsufficientFunds_LeavesNothingBehind` |
| The database is unreachable at startup | The server exits with an error and does not start half-working | checked manually |
| SIGTERM or Ctrl-C while serving | The server stops accepting connections, finishes in-flight requests (up to `SHUTDOWN_TIMEOUT`), then closes the pool | checked manually, not under load |

The crash test simulates the failure by returning an error mid-transaction. It does not kill the process with `kill -9`. Rolling back an uncommitted transaction is PostgreSQL's own behaviour, and this test shows the service relies on it correctly.

## Load test results

20 concurrent clients for 30 seconds, using k6. The k6 client, the app, and PostgreSQL all ran on the same laptop (WSL2 with Docker Desktop), so they competed for the same CPU. Hardware: **[add your CPU model and RAM here]**.

| Scenario | Transfers | Throughput | Median | p95 | p99 | Max | Errors |
|---|---|---|---|---|---|---|---|
| **spread**: random pairs out of 200 accounts | 30,925 | **972 /s** | 15.2 ms | 39.5 ms | **73.9 ms** | 244 ms | 0 |
| **hot**: every transfer between the same 2 accounts | 5,187 | **172 /s** | 17.7 ms | 544 ms | **1.28 s** | 3.3 s | 0 |

After each run the test reads every account back and fails if the total no longer matches what was deposited. Both runs reported `PASS: the books balance after the load test`.

**What the numbers say.** Spreading load across accounts gave 5.6 times the throughput and a p99 17 times lower. The median is almost the same in both runs, which points to queuing: a transfer that gets the lock is just as fast, and the ones queued behind up to 19 others make up the tail.

Because every transfer holds a row lock until it commits, throughput on a single account is limited by how long one transfer takes. Here that ceiling is about 170 transfers per second, which suggests roughly 6 ms per transfer including the commit (an estimate from these numbers, not a measurement).

**What I would do about it, not implemented:** cut the number of database round trips made while the lock is held, and split a very busy account into several sub-accounts that are summed on read.

**An observation I have not explained:** throughput dipped for several seconds in the middle of both runs, and the hot run slowed to roughly 25 requests per second for about six seconds. Candidates are a PostgreSQL checkpoint or Docker Desktop CPU limits. I did not investigate.

```bash
make load-spread     # realistic: random accounts
make load-hot        # worst case: one pair of accounts
make load-dup        # a small pool of idempotency keys reused constantly
```

## Run it

You need Go, Docker, and make.

```bash
# database only, develop with go run
docker compose up -d
export DATABASE_URL='postgres://ledger:ledger@localhost:5432/ledgerlite'
LOG_FORMAT=text go run ./cmd/server

# tests (the database must be running)
make test

# everything: app, Prometheus, Grafana
docker compose --profile stack up -d --build
```

With the full stack running, the app is at http://localhost:8080, Prometheus at http://localhost:9090, and Grafana at http://localhost:3000 (dashboard: `/d/ledgerlite`). The database credentials in `docker-compose.yml` are for local development only.

### Configuration

| Variable | Default | Meaning |
|---|---|---|
| `DATABASE_URL` | required | PostgreSQL connection string |
| `HTTP_ADDR` | `:8080` | Listen address. If unset, `PORT` is used as `:<PORT>` |
| `DB_MAX_CONNS` | `20` | Connection pool size |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `LOG_FORMAT` | `json` | `json` or `text` |
| `SHUTDOWN_TIMEOUT` | `15s` | How long to wait for in-flight requests on shutdown |

## API

All amounts are integers in paise (`50000` = ₹500.00). Money never uses floating point.

| Method | Path | Notes |
|---|---|---|
| POST | `/v1/accounts` | `{"name": "alice"}` |
| GET | `/v1/accounts/{id}` | Balance and details |
| POST | `/v1/deposits` | `{"to": "<id>", "amount": 50000}`, needs `Idempotency-Key` |
| POST | `/v1/transfers` | `{"from": "<id>", "to": "<id>", "amount": 8000}`, needs `Idempotency-Key` |
| GET | `/healthz` | Liveness |
| GET | `/metrics` | Prometheus metrics |

```bash
curl -i -X POST localhost:8080/v1/transfers \
  -H 'Idempotency-Key: pay-1042' \
  -d '{"from":"<alice-id>","to":"<bob-id>","amount":8000}'
```

`201` means the request executed. `200` with `Idempotent-Replayed: true` means it was a retry and no money moved. Optionally send `X-Client-ID` to scope idempotency keys per client.

| Status | Error code | Meaning |
|---|---|---|
| 400 | `invalid_json`, `invalid_account_id`, `invalid_amount`, `same_account`, `missing_idempotency_key`, `invalid_name`, `invalid_header` | Bad request |
| 403 | `forbidden_account` | The internal `external` account cannot be used through the transfer API |
| 404 | `account_not_found` | |
| 422 | `insufficient_funds`, `idempotency_key_reused` | Valid request that cannot be applied |
| 500 | `internal_error` | Details are logged, never returned to the client |

## Design decisions

- **PostgreSQL only, no Redis.** The ledger needs transactions, constraints, and row locks, and PostgreSQL provides all three. Caching a balance would only add a way to be wrong: a stale balance is a correctness bug.
- **`READ COMMITTED` with explicit row locks**, not `SERIALIZABLE`. The locks make the check-then-update safe, and holding them only for the length of one short transaction keeps the cost low.
- **Cached balance plus append-only entries.** The `balance` column is a fast read. The entries are the source of truth, and the load test and integration tests verify that the two always agree.
- **Idempotency key inside the transaction.** A concurrent duplicate waits on the unique index until the first transaction finishes, then replays its result. A failed request rolls its key back, so it can be retried.
- **Metrics are labelled by route pattern**, not by URL, so account ids cannot create unbounded label values.
- **IDs are UUIDv7**, time-ordered and generated by PostgreSQL 18, so they also give a stable lock order.

## Limitations

- **No authentication.** `X-Client-ID` is self-declared and only scopes idempotency keys.
- **The outbox is written but not published.** Events are recorded in the same transaction as each transfer, but nothing reads them yet. A publisher is the next planned project.
- **No history endpoint.** Entries exist, but there is no API to list an account's statement.
- **Idempotency keys never expire.** There is no cleanup job, so the table grows.
- **One currency.** Accounts have a currency column, but nothing converts or checks it.
- **A single hot account is bounded** by row locking, as the load test shows.
- **Immutability is enforced by triggers**, which protect against application bugs. A database administrator can still disable them.
- **`/metrics` shares the public port.** In production it belongs on a separate internal one.
- **Tests run locally** against a PostgreSQL container; there is no CI pipeline yet.