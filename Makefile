.PHONY: run build build-pubsub clean test test-functional test-smoke test-regression tidy migration.up migration.down migration.drop

# Load DATABASE_URL from .env if not already set in the environment
DB_URL ?= $(or $(DATABASE_URL),$(shell grep -E '^DATABASE_URL=' .env 2>/dev/null | cut -d '=' -f2-))
MIGRATIONS_DIR := internal/migrations

ifneq ($(filter test-functional test-smoke test-regression,$(MAKECMDGOALS)),)
-include .env.test
endif

SMOKE_BASE_URL ?= http://localhost:8080

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

## test-functional: run smoke and PostgreSQL regression tests
test-functional: test-smoke test-regression

## test-smoke: check health and signed-out route protection on a running API
test-smoke: export SMOKE_BASE_URL := $(SMOKE_BASE_URL)
test-smoke:
	@test -n "$$SMOKE_BASE_URL" || { echo "Set SMOKE_BASE_URL to the running API origin."; exit 1; }
	go test ./cmd/server -run '^TestAPISmoke$$' -count=1 -timeout=2m -v

## test-regression: run Go tests and PostgreSQL integration tests
test-regression: export RBAC_TEST_DATABASE_URL := $(RBAC_TEST_DATABASE_URL)
test-regression:
	@test -n "$$RBAC_TEST_DATABASE_URL" || { echo "Set RBAC_TEST_DATABASE_URL in .env.test to a disposable PostgreSQL database."; exit 1; }
	SMOKE_BASE_URL= go test -count=1 ./...

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
