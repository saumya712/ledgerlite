// Load test for Ledgerlite. One script, three scenarios, chosen with SCENARIO:
//
//   hot         every request moves money between the SAME two accounts.
//               They all queue on one row lock: the worst case.
//   spread      every request picks two random accounts out of many.
//               The realistic case: transactions rarely touch the same rows.
//   duplicates  a small pool of idempotency keys is reused over and over.
//               Only the first request per key may move money.
//
// After the run, teardown() reads every account back and FAILS the test if the
// books do not balance, so the load test checks correctness, not only speed.
//
// Settings (environment variables, all optional):
//   BASE_URL (http://localhost:8080)   VUS (20)   DURATION (30s)
//   ACCOUNTS (200, spread only)        DUP_KEYS (50, duplicates only)

import http from 'k6/http';
import { check } from 'k6';
import exec from 'k6/execution';
import { Counter } from 'k6/metrics';

const BASE = __ENV.BASE_URL || 'http://localhost:8080';
const SCENARIO = __ENV.SCENARIO || 'spread';
const VUS = parseInt(__ENV.VUS || '20', 10);
const DURATION = __ENV.DURATION || '30s';
const SPREAD_ACCOUNTS = parseInt(__ENV.ACCOUNTS || '200', 10);
const DUP_KEYS = parseInt(__ENV.DUP_KEYS || '50', 10);

const AMOUNT = 100;          // paise moved by each transfer (Rs 1.00)
const FUNDING = 1000000000;  // paise each account starts with (Rs 10,000,000)

if (!['hot', 'spread', 'duplicates'].includes(SCENARIO)) {
  throw new Error(`SCENARIO must be hot, spread or duplicates, got "${SCENARIO}"`);
}

// Pass/fail targets. These are starting points: measure first, then tighten.
const LIMITS = {
  hot: ['p(95)<2000', 'p(99)<3000'],
  spread: ['p(95)<500', 'p(99)<1000'],
  duplicates: ['p(95)<2000', 'p(99)<3000'],
};

export const options = {
  scenarios: {
    [SCENARIO]: {
      executor: 'constant-vus',   // fixed number of concurrent clients
      vus: VUS,
      duration: DURATION,
      exec: SCENARIO,             // the exported function below with this name
    },
  },
  thresholds: {
    'http_req_failed{name:transfer}': ['rate<0.01'],
    'http_req_duration{name:transfer}': LIMITS[SCENARIO],
    checks: ['rate>0.99'],
  },
  summaryTrendStats: ['avg', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
};

const replays = new Counter('ledger_replayed_responses');

function post(path, body, key, name) {
  const headers = { 'Content-Type': 'application/json' };
  if (key) headers['Idempotency-Key'] = key;
  return http.post(BASE + path, JSON.stringify(body), { headers, tags: { name } });
}

// ---- setup: runs once, before the load ----

export function setup() {
  const runId = String(Date.now());          // keeps idempotency keys unique across runs
  const count = SCENARIO === 'spread' ? SPREAD_ACCOUNTS : 2;
  const accounts = [];

  for (let i = 0; i < count; i++) {
    const created = post('/v1/accounts', { name: `load-${runId}-${i}` }, null, 'setup');
    if (created.status !== 201) {
      throw new Error(`cannot create account: HTTP ${created.status} ${created.body}`);
    }
    const id = created.json('id');
    const funded = post('/v1/deposits', { to: id, amount: FUNDING }, `${runId}-fund-${i}`, 'setup');
    if (funded.status !== 201) {
      throw new Error(`cannot fund account: HTTP ${funded.status} ${funded.body}`);
    }
    accounts.push(id);
  }
  return { runId, accounts, funded: count * FUNDING };
}

// ---- the three scenarios ----

function transfer(from, to, key, okStatuses) {
  const res = post('/v1/transfers', { from, to, amount: AMOUNT }, key, 'transfer');
  if (res.headers['Idempotent-Replayed'] === 'true') replays.add(1);
  check(res, { 'transfer accepted': (r) => okStatuses.includes(r.status) });
}

export function hot(data) {
  const key = `${data.runId}-${exec.scenario.iterationInTest}`;
  transfer(data.accounts[0], data.accounts[1], key, [201]);
}

export function spread(data) {
  const n = data.accounts.length;
  const a = Math.floor(Math.random() * n);
  let b = Math.floor(Math.random() * (n - 1));
  if (b >= a) b++;                            // a different account than a
  const key = `${data.runId}-${exec.scenario.iterationInTest}`;
  transfer(data.accounts[a], data.accounts[b], key, [201]);
}

export function duplicates(data) {
  // iteration 0..49 use keys 0..49 (executed); every later iteration repeats one (replayed)
  const k = exec.scenario.iterationInTest % DUP_KEYS;
  transfer(data.accounts[0], data.accounts[1], `${data.runId}-dup-${k}`, [200, 201]);
}

// ---- teardown: runs once, after the load. Throws if anything is wrong. ----

export function teardown(data) {
  let sum = 0;
  const balances = [];
  for (const id of data.accounts) {
    const res = http.get(`${BASE}/v1/accounts/${id}`, { tags: { name: 'verify' } });
    if (res.status !== 200) throw new Error(`cannot read account ${id}: HTTP ${res.status}`);
    const balance = res.json('balance');
    balances.push(balance);
    sum += balance;
  }

  console.log(`books check: accounts=${data.accounts.length} funded=${data.funded} sum_now=${sum}`);
  if (sum !== data.funded) {
    throw new Error(`BOOKS DO NOT BALANCE: funded ${data.funded}, accounts now hold ${sum}`);
  }

  if (SCENARIO === 'duplicates') {
    const expected = FUNDING - DUP_KEYS * AMOUNT;
    console.log(`duplicates check: source=${balances[0]} expected=${expected} (${DUP_KEYS} unique keys)`);
    if (balances[0] !== expected) {
      throw new Error(`DUPLICATES MOVED MONEY MORE THAN ONCE: source=${balances[0]}, expected ${expected}`);
    }
  }
  console.log('PASS: the books balance after the load test');
}
