-include .env
export

MIGRATIONS_DIR := migrations

.PHONY: run build test cover vet fmt lint vuln tidy up down migrate-up migrate-down migrate-test check

run:
	go run ./cmd/api

build:
	go build -o bin/api ./cmd/api

test:
	go test -race ./...

cover:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -n 1

vet:
	go vet ./...

fmt:
	gofmt -l -w .

lint: vet
	@command -v golangci-lint >/dev/null && golangci-lint run || echo "golangci-lint not installed (brew install golangci-lint); ran go vet only"

vuln:
	govulncheck ./...

tidy:
	go mod tidy

up:
	docker compose up -d

down:
	docker compose down

migrate-up:
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" up

# DESTRUCTIVE: drops everything the migrations created
migrate-down:
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" down -all

# Proves the migration is reversible: up, down, up again
migrate-test:
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" up
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" down -all
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" up

check: fmt vet test
