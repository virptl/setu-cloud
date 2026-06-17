BIN       := bin/setu-cloud
ADMIN_BIN := bin/setu-admin

-include .env
export

MIGRATE := goose -dir migrations postgres "$(DATABASE_URL)"

.PHONY: build run build-admin admin migrate-up migrate-down migrate-status tidy

build:
	go build -o $(BIN) ./cmd/server

run: build
	./$(BIN)

build-admin:
	go build -o $(ADMIN_BIN) ./admin

admin: build-admin
	./$(ADMIN_BIN)

create-admin: build-admin
	./$(ADMIN_BIN) --create-admin $(ADMIN_USER)

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
