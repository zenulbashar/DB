# NimbusDB monorepo entry points. Each target delegates into the owning package.

.PHONY: dev dev-db migrate api test test-integration lint build console-dev console-build generate openapi-lint

## Local development ---------------------------------------------------------

dev-db: ## Start local control-plane Postgres (docker compose)
	docker compose up -d postgres

dev: dev-db migrate ## Run the control-plane API against local Postgres
	cd services/control-plane && DATABASE_URL=$${DATABASE_URL:-postgres://ndb_app:ndb_app@localhost:5433/nimbusdb_cp?sslmode=disable} \
		NDB_BOOTSTRAP_TOKEN=$${NDB_BOOTSTRAP_TOKEN:-dev-bootstrap-token} \
		go run ./cmd/api

migrate: ## Apply control-plane migrations
	cd services/control-plane && DATABASE_URL=$${DATABASE_URL:-postgres://ndb_app:ndb_app@localhost:5433/nimbusdb_cp?sslmode=disable} \
		go run ./cmd/api -migrate-only

console-dev:
	cd console && npm run dev

## Quality gates --------------------------------------------------------------

test: ## Unit tests (no external dependencies)
	cd services/control-plane && go vet ./... && go test ./...
	cd services/pg-gateway && go vet ./... && go test ./...

test-integration: ## Integration tests; requires TEST_DATABASE_URL pointing at a scratch Postgres
	cd services/control-plane && go test -tags=integration ./internal/store/postgres/...
	cd services/pg-gateway && go test -tags=integration ./...

build:
	cd services/control-plane && go build ./...
	cd services/pg-gateway && go build ./...

gateway-dev: ## Run pg-gateway locally (expects PGGW_* env; see services/pg-gateway/cmd/gateway)
	cd services/pg-gateway && go run ./cmd/gateway

console-build:
	cd console && npm run build

generate: ## Regenerate the TS API client from api/openapi.yaml
	cd packages/api-client && npm run generate

openapi-lint:
	npx --yes @redocly/cli@latest lint api/openapi.yaml
