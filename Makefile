BIN     := bin/setu-cloud

-include .env
export

MIGRATE := goose -dir migrations postgres "$(DATABASE_URL)"

.PHONY: build run migrate-up migrate-down migrate-status tidy

build:
	go build -o $(BIN) ./cmd/server

run: build
	./$(BIN)

tidy:
	go mod tidy

migrate-up:
	$(MIGRATE) up

migrate-down:
	$(MIGRATE) down

migrate-status:
	$(MIGRATE) status

migrate-create:
	goose -dir migrations create $(name) sql
