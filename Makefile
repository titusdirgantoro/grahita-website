.DEFAULT_GOAL := help
COMPOSE := docker compose

help: ## Menampilkan daftar perintah
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

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

migrate: ## Menjalankan migrasi database
	$(COMPOSE) run --rm migrate

seed: ## Mengisi data contoh untuk development
	$(COMPOSE) run --rm migrate --mode=seed

psql: ## Membuka psql di dalam container
	$(COMPOSE) exec postgres psql -U $${POSTGRES_USER:-grahita} -d $${POSTGRES_DB:-grahita}

smoke: ## Mengecek seluruh service benar-benar hidup
	@./scripts/smoke.sh

rebuild: ## Build ulang image api/worker tanpa cache
	$(COMPOSE) build --no-cache api

.PHONY: help up down reset logs ps migrate seed psql smoke rebuild
