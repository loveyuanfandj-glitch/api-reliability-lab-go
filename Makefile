.DEFAULT_GOAL := help

.PHONY: help fmt fmt-check vet test test-race build run clean docker-build up down logs smoke idempotency dependency-recovery rate-limit websocket-replay demo validate

help: ## Show available commands.
	@awk 'BEGIN {FS = ":.*## "; printf "Northstar Reliability Lab\n\n"} /^[a-zA-Z_-]+:.*## / {printf "  %-22s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

fmt: ## Format Go source files.
	gofmt -w $$(find . -name '*.go' -not -path './.git/*')

fmt-check: ## Fail if Go source is not formatted.
	@test -z "$$(gofmt -l .)"

vet: ## Run Go static analysis.
	go vet ./...

test: ## Run unit and integration tests.
	go test ./...

test-race: ## Run tests with the race detector.
	go test -race ./...

build: ## Build the server binary.
	go build -trimpath -o bin/northstar-api ./cmd/server

run: ## Run the API locally.
	DEMO_MODE=true go run ./cmd/server

clean: ## Remove local build output.
	rm -f bin/northstar-api coverage.out

docker-build: ## Build the production container.
	docker build -t northstar-api:local .

up: ## Start API, Prometheus, and Grafana.
	docker compose up --build --detach

down: ## Stop the lab while preserving metric and dashboard volumes.
	docker compose down

logs: ## Follow API logs.
	docker compose logs --follow api

smoke: ## Run a short healthy-path load test.
	docker compose --profile load run --rm k6 run /tests/smoke.js

idempotency: ## Prove concurrent duplicate suppression.
	docker compose --profile load run --rm k6 run /tests/idempotency.js

dependency-recovery: ## Open and recover the dependency circuit.
	docker compose --profile load run --rm k6 run /tests/dependency-recovery.js

rate-limit: ## Drive one synthetic tenant into its rate limit.
	docker compose --profile load run --rm k6 run /tests/rate-limit.js

websocket-replay: ## Verify ordered gap recovery after reconnect.
	docker compose --profile load run --rm k6 run /tests/websocket-replay.js

demo: ## Run the narrated curl-based reliability demonstration.
	./scripts/demo.sh

validate: fmt-check vet test ## Validate code, Compose, and dashboard assets.
	docker compose config -q
	jq empty deployments/grafana/dashboards/*.json
