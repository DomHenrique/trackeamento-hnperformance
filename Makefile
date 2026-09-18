.PHONY: all build tidy test run-api run-ingester run-dispatcher docker-up docker-down

GO ?= $(shell which go 2>/dev/null || echo "$$HOME/.local/go/bin/go")

all: build

tidy:
	$(GO) mod tidy

build: tidy
	@echo "==> Compilando binários Go..."
	mkdir -p bin
	$(GO) build -o bin/api ./cmd/api
	$(GO) build -o bin/ingester ./cmd/ingester
	$(GO) build -o bin/dispatcher ./cmd/dispatcher
	@echo "==> Build concluído com sucesso!"

test:
	$(GO) test -v ./...

docker-up:
	docker compose up -d redis postgres clickhouse traefik

docker-down:
	docker compose down

run-api:
	$(GO) run ./cmd/api

run-ingester:
	$(GO) run ./cmd/ingester

run-dispatcher:
	$(GO) run ./cmd/dispatcher
