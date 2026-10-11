.PHONY: up down test load-hot load-spread load-dup load-all

# Compose names its network <folder>_default. Change this if your folder is not "ledgerlite".
NETWORK  ?= ledgerlite_default
BASE_URL ?= http://app:8080
VUS      ?= 20
DURATION ?= 30s

# k6 runs in a container on the same network as the app, so there is nothing to install.
K6 = docker run --rm -i --network $(NETWORK) -e BASE_URL=$(BASE_URL) -e VUS=$(VUS) -e DURATION=$(DURATION) -v "$(CURDIR)/loadtest:/scripts:ro"

up: ## database + app + Prometheus + Grafana
	docker compose --profile stack up -d --build

down:
	docker compose --profile stack down

test: ## unit + integration tests (needs the database running)
	go vet ./... && go test ./internal/... -count=1 && go test ./test/ -count=1

load-hot: ## every transfer between the SAME two accounts
	$(K6) -e SCENARIO=hot grafana/k6 run /scripts/ledger.js

load-spread: ## transfers between random accounts out of 200
	$(K6) -e SCENARIO=spread grafana/k6 run /scripts/ledger.js

load-dup: ## a small pool of idempotency keys reused constantly
	$(K6) -e SCENARIO=duplicates grafana/k6 run /scripts/ledger.js

load-all: load-hot load-spread load-dup