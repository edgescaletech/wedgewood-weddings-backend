# Wedgewood Weddings Backend

Go HTTP API and Salesforce integration for the venue scheduling prototype.

## Run locally

1. Install the Go version listed in `go.mod`.
2. Copy `.env.example` to `.env` and add Salesforce credentials. **Do not commit `.env`.**
3. Start the API:

   ```sh
   make run
   ```

   The API listens on `http://localhost:8080` by default. Useful routes:

   - `GET /healthz` — health check
   - `GET /api/events?week=YYYY-MM-DD` — event data for a week
   - `GET /docs` — API documentation
   - `GET /openapi.yaml` — OpenAPI specification

The Salesforce Pub/Sub subscriber can be run separately with `go run ./cmd/pubsub/` after configuring its required environment variables.

## Security note

This is a prototype. The `/api/auth/me` route currently returns demo identity/role data; it is not production authentication or authorization. Do not rely on it to protect real user or venue data.

The database, production login/permissions, and ADP workflows are still under design. Keep credentials in local environment variables or the deployment secret manager, never in source control.
