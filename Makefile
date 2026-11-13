.PHONY: run build build-pubsub clean test tidy migration.up migration.down migration.drop

# Load DATABASE_URL from .env if not already set in the environment
DB_URL ?= $(or $(DATABASE_URL),$(shell grep -E '^DATABASE_URL=' .env 2>/dev/null | cut -d '=' -f2-))
MIGRATIONS_DIR := internal/migrations

## run: start the HTTP server locally (loads .env automatically)
run:
	go run ./cmd/server/

## build: compile the main HTTP server binary
build:
	go build -o server ./cmd/server/

## build-pubsub: compile the Pub/Sub subscriber binary
build-pubsub:
	go build -o pubsub ./cmd/pubsub/

## test: run all tests
test:
	go test ./...

## tidy: clean up go.mod and go.sum
tidy:
	go mod tidy

## clean: remove compiled binaries
clean:
	rm -f server pubsub

## migrate: apply PostgreSQL migrations (requires DATABASE_URL)
migrate:
	go run ./cmd/migrate
## migration.up: apply all pending migrations to the database
migration.up:
	migrate -path $(MIGRATIONS_DIR) -database "$(DB_URL)" up

## migration.down: roll back the last applied migration
migration.down:
	migrate -path $(MIGRATIONS_DIR) -database "$(DB_URL)" down 1

## migration.drop: drop everything in the database (use with caution!)
migration.drop:
	migrate -path $(MIGRATIONS_DIR) -database "$(DB_URL)" drop -f
