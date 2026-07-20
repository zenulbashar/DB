# NimbusDB monorepo entry points. Each target delegates into the owning package.

.PHONY: dev dev-db migrate api test test-integration lint build console-dev console-build generate openapi-lint smoke

## Local development ---------------------------------------------------------

dev-db: ## Start local control-plane Postgres (docker compose)
	docker compose up -d postgres

# Dev-only KEK (base64 of 32 zero bytes is NOT used — this is a fixed random
# dev key; production keys come from KMS/secret store, never from a Makefile).
DEV_KEK := 1:XBMsCi/pCq8xMYM9KP0xSjO/O8sB9oYLFqOUoHy+E10=

dev: dev-db migrate ## Run the control-plane API against local Postgres
	cd services/control-plane && DATABASE_URL=$${DATABASE_URL:-postgres://ndb_app:ndb_app@localhost:5433/nimbusdb_cp?sslmode=disable} \
		NDB_BOOTSTRAP_TOKEN=$${NDB_BOOTSTRAP_TOKEN:-dev-bootstrap-token} \
		NDB_ADMIN_TOKEN=$${NDB_ADMIN_TOKEN:-dev-admin-token} \
		NDB_KEKS=$${NDB_KEKS:-$(DEV_KEK)} \
		go run ./cmd/api

migrate: ## Apply control-plane migrations
	cd services/control-plane && DATABASE_URL=$${DATABASE_URL:-postgres://ndb_app:ndb_app@localhost:5433/nimbusdb_cp?sslmode=disable} \
		go run ./cmd/api -migrate-only

console-dev:
	cd console && npm run dev

smoke: ## End-to-end smoke test: console renders live control-plane data (needs a FRESH DATABASE_URL)
	tools/smoke-e2e.sh

## Quality gates --------------------------------------------------------------

GO_MODULES := services/control-plane services/pg-gateway services/import-engine

test: ## Unit tests (no external dependencies)
	@for m in $(GO_MODULES); do (cd $$m && go vet ./... && go test ./...) || exit 1; done

test-integration: ## Integration tests; requires TEST_DATABASE_URL pointing at a scratch Postgres
	cd services/control-plane && go test -tags=integration ./internal/store/postgres/...
	cd services/pg-gateway && go test -tags=integration ./...
	cd services/import-engine && go test -tags=integration ./...

build:
	@for m in $(GO_MODULES); do (cd $$m && go build ./...) || exit 1; done

gateway-dev: ## Run pg-gateway locally (expects PGGW_* env; see services/pg-gateway/cmd/gateway)
	cd services/pg-gateway && go run ./cmd/gateway

console-build:
	cd console && npm run build

generate: ## Regenerate the TS API client from api/openapi.yaml
	cd packages/api-client && npm run generate

openapi-lint:
	npx --yes @redocly/cli@latest lint api/openapi.yaml
