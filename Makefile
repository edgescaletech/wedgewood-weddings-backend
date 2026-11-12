.PHONY: run build build-pubsub clean test tidy

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
