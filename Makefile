.DEFAULT_GOAL := help
COMPOSE := docker compose
API_DIR := api
WEB_DIR := web

## ---------- toolchain (eksplisit & repeatable; lihat scripts/go-env.sh) ----------
## Runner uji tidak selalu punya `go` di PATH (kartu t_ac58a28e: `make test`
## berhenti dengan `/bin/sh: 1: go: not found`). Pemilih toolchain memilih Go
## >= 1.27 secara deterministik tanpa mengandalkan PATH interaktif, lalu
## direktorinya ditambahkan ke PATH seluruh resep supaya golangci-lint/gofmt
## yang memanggil `go` juga menemukannya. Override: make test GO=/path/ke/go
##
## FAIL-CLOSED (cacat fail-open QA putaran 2): nilai pemilih diambil SEKALI
## (`:=` di bawah `ifndef`, bukan `$(shell)` rekursif yang di-expand berulang),
## dan setiap resep yang memakai toolchain melewati penjaga `_wajib_go` /
## `_wajib_lint` lebih dulu. Tanpa penjaga, `$(GO)` yang kosong membuat
## `cd api && $(GO) test ./...` merosot menjadi perintah shell builtin
## `test ./...` yang SELALU true -> `make test` hijau tanpa satu tes pun.
SKRIP_TOOLCHAIN := $(dir $(lastword $(MAKEFILE_LIST)))scripts/go-env.sh
ifndef GO
GO := $(shell $(SKRIP_TOOLCHAIN) --go)
endif
ifndef GOLANGCI_LINT
GOLANGCI_LINT := $(shell $(SKRIP_TOOLCHAIN) --golangci-lint)
endif
TOOLCHAIN_PATH := $(if $(filter /%,$(GO)),$(patsubst %/,%,$(dir $(GO))):,)
TOOLCHAIN_PATH := $(TOOLCHAIN_PATH)$(if $(filter /%,$(GOLANGCI_LINT)),$(patsubst %/,%,$(dir $(GOLANGCI_LINT))):,)
export PATH := $(TOOLCHAIN_PATH)$(PATH)

# Penjaga fail-closed. Dijalankan sebagai baris PERTAMA resep yang butuh
# toolchain sehingga `GO=` / `GOLANGCI_LINT=` kosong — dari lingkungan maupun
# command line — tetap tertangkap: berhenti dengan pesan jelas + exit 2.
define _wajib_go
@test -n "$(GO)" || { printf '%s\n' "GALAT toolchain: biner Go tidak teresolusi (GO kosong)." "Jalankan ./scripts/runner-provision.sh, atau set GRAHITA_GO=/jalur/ke/go (lihat scripts/go-env.sh --help)." >&2; exit 2; }
endef
define _wajib_lint
@test -n "$(GOLANGCI_LINT)" || { printf '%s\n' "GALAT toolchain: golangci-lint tidak teresolusi (GOLANGCI_LINT kosong)." "Set GRAHITA_GOLANGCI_LINT=/jalur/ke/golangci-lint (lihat scripts/go-env.sh --help)." >&2; exit 2; }
endef

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
	$(_wajib_go)
	cd $(API_DIR) && $(GO) vet ./...

test: ## Uji unit backend
	$(_wajib_go)
	cd $(API_DIR) && $(GO) test ./...

test-integrasi: ## Uji integrasi (butuh TEST_DATABASE_URL menunjuk PG bersih)
	$(_wajib_go)
	cd $(API_DIR) && $(GO) test -p 1 -count=1 -timeout 60m ./internal/platform/database/... ./internal/worker/... ./internal/httpapi/...

build: ## Build binary server
	$(_wajib_go)
	cd $(API_DIR) && $(GO) build -o ../bin/server ./cmd/server

sqlc: ## Membangkitkan query sqlc
	cd $(API_DIR) && sqlc generate

lint: ## golangci-lint (jalur biner eksplisit, lihat scripts/go-env.sh)
	$(_wajib_go)
	$(_wajib_lint)
	cd $(API_DIR) && $(GOLANGCI_LINT) run ./...

toolchain-cek: ## Laporkan toolchain Go/lint yang dipakai resep Makefile
	$(_wajib_go)
	$(_wajib_lint)
	@echo "go            : $(GO)"
	@echo "golangci-lint : $(GOLANGCI_LINT)"
	@echo "PATH toolchain: $(TOOLCHAIN_PATH)"
	@$(GO) version
	@$(GOLANGCI_LINT) version
	@$(SKRIP_TOOLCHAIN) --cek

lint-diri: ## Kendali positif+negatif pemilih toolchain (scripts/go-env.sh --uji-diri)
	$(SKRIP_TOOLCHAIN) --uji-diri

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

uji-rahasia: ## Kendali diri pemeriksa rahasia (kontrol negatif + positif)
	./scripts/check-secrets.sh --uji-diri

migrasi-cek: ## Gerbang migrasi kompatibel mundur (Architecture.md §9)
	./scripts/check-migration-kompatibel.sh

smoke-staging: ## Uji asap alur kritis pada staging/produksi (butuh env kredensial)
	./scripts/smoke-staging.sh

## ---------- operasional (F1-22) ----------

observability-up: ## Menjalankan Prometheus/Alertmanager/Loki/Promtail/Grafana
	CUSTOMER_HOST=$${CUSTOMER_HOST:-grahita-branch.tiusclaw.com} ADMIN_HOST=$${ADMIN_HOST:-grahita-admin.tiusclaw.com} \
	POSTGRES_PASSWORD=$${POSTGRES_PASSWORD:?} SMTP_USER=$${SMTP_USER:?} SMTP_PASS=$${SMTP_PASS:?} \
	$(COMPOSE) -f docker-compose.yml -f docker-compose.prod.yml -f docker-compose.observability.yml up -d

observability-caddy-cek: ## Validasi Caddyfile produksi/staging (butuh biner caddy)
	caddy validate --config infra/caddy/Caddyfile.prod --adapter caddyfile

observability-prom-cek: ## Validasi konfigurasi & aturan Prometheus (butuh promtool)
	promtool check config infra/observability/prometheus/prometheus.yml
	promtool check rules infra/observability/prometheus/rules/grahita-alerts.yml

observability-grafana-cek: ## Validasi uid sumber data dasbor Grafana vs provisioning
	python3 scripts/cek-uid-grafana.py .

ci-lokal: fmt vet lint test web-typecheck web-test web-build openapi secrets migrasi-cek ## Menjalankan gerbang CI secara lokal

.PHONY: help up down reset logs ps migrate seed psql smoke fmt vet test test-integrasi \
        build sqlc lint toolchain-cek lint-diri shim-pg web-install web-dev-customer \
        web-dev-admin web-typecheck web-test web-build openapi secrets migrasi-cek \
        smoke-staging observability-up observability-caddy-cek observability-prom-cek \
        observability-grafana-cek ci-lokal
