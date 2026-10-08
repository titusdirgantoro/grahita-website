.DEFAULT_GOAL := help
COMPOSE := docker compose
API_DIR := api
WEB_DIR := web

help: ## Menampilkan daftar perintah
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

## ---------- lingkungan lokal ----------

up: ## Menjalankan seluruh stack (build bila perlu)
	$(COMPOSE) up -d --build
	@echo ""
	@echo "  Customer Portal : http://localhost:8080"
	@echo "  Admin Portal    : http://localhost:8081"
	@echo "  API             : http://localhost:8088/healthz"
	@echo "  Mailpit (email) : http://localhost:8025"
	@echo "  MinIO console   : http://localhost:9001"
	@echo ""

down: ## Menghentikan stack (data tetap ada)
	$(COMPOSE) down

reset: ## Menghentikan stack DAN menghapus seluruh data
	$(COMPOSE) down -v

logs: ## Mengikuti log api dan worker
	$(COMPOSE) logs -f api worker

ps: ## Status service
	$(COMPOSE) ps

migrate: ## Menjalankan migrasi goose + tabel River
	$(COMPOSE) run --rm migrate

seed: ## Mengisi data contoh (butuh SEED_PASSWORD di environment)
	$(COMPOSE) run --rm -e SEED_PASSWORD=$(SEED_PASSWORD) seed

psql: ## Membuka psql di dalam container
	$(COMPOSE) exec postgres psql -U $${POSTGRES_USER:-grahita} -d $${POSTGRES_DB:-grahita}

smoke: ## Memeriksa seluruh service benar-benar hidup
	@./scripts/smoke.sh

## ---------- backend ----------

fmt: ## gofmt seluruh kode Go
	cd $(API_DIR) && gofmt -l -w .

vet: ## go vet
	cd $(API_DIR) && go vet ./...

test: ## Uji unit backend
	cd $(API_DIR) && go test ./...

test-integrasi: ## Uji integrasi (butuh TEST_DATABASE_URL menunjuk PG bersih)
	cd $(API_DIR) && go test -count=1 ./internal/platform/database/... ./internal/worker/...

build: ## Build binary server
	cd $(API_DIR) && go build -o ../bin/server ./cmd/server

sqlc: ## Membangkitkan query sqlc
	cd $(API_DIR) && sqlc generate

shim-pg: ## Terapkan shim uuidv7 untuk PostgreSQL < 18 (khusus lokal)
	psql "$$TEST_DATABASE_URL" -f scripts/pg-dev-shim.sql

## ---------- frontend ----------

web-install: ## Memasang dependensi SPA
	cd $(WEB_DIR) && npm ci --no-audit --no-fund

web-dev-customer: ## Dev server portal cabang
	cd $(WEB_DIR) && npm run dev:customer

web-dev-admin: ## Dev server portal admin
	cd $(WEB_DIR) && npm run dev:admin

web-typecheck: ## tsc --noEmit kedua SPA
	cd $(WEB_DIR) && npm run typecheck

web-test: ## vitest
	cd $(WEB_DIR) && npm run test

web-build: ## Build produksi kedua SPA
	cd $(WEB_DIR) && npm run build

## ---------- kontrak & keamanan ----------

openapi: ## Lint skema OpenAPI
	npx --yes @redocly/cli@1 lint openapi/openapi.yaml

secrets: ## Memindai pola rahasia di repo
	./scripts/check-secrets.sh

ci-lokal: fmt vet test web-typecheck web-test web-build openapi secrets ## Menjalankan gerbang CI secara lokal

.PHONY: help up down reset logs ps migrate seed psql smoke fmt vet test test-integrasi \
        build sqlc shim-pg web-install web-dev-customer web-dev-admin web-typecheck \
        web-test web-build openapi secrets ci-lokal
