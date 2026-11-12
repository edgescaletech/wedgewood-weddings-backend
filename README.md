# Wedgewood Weddings Backend

Go HTTP API and Salesforce integration for the venue scheduling prototype.

## Run locally

1. Install the Go version listed in `go.mod`.
2. Copy `.env.example` to `.env` and add Salesforce credentials. **Do not commit `.env`.**
3. If using PostgreSQL, apply migrations as described below.
4. Start the API:

   ```sh
   make run
   ```

   The API listens on `http://localhost:8080` by default. Useful routes:

   - `GET /healthz` — health check
   - `GET /api/events?week=YYYY-MM-DD` — event data for a week
   - `GET /docs` — API documentation
   - `GET /openapi.yaml` — OpenAPI specification

The Salesforce Pub/Sub subscriber can be run separately with `go run ./cmd/pubsub/` after configuring its required environment variables.

## Database migrations

Install the golang-migrate CLI with PostgreSQL support. From the backend repository root, configure the target environment’s `DATABASE_URL` and run:

```sh
make migration.up
```

Run this as a separate step for each dev, staging, and production database before deploying an API version that requires a new migration. If the command fails, stop the rollout. The API does not apply migrations at startup. Render Blueprint auto deploy is disabled so the service can be deployed manually after migrations succeed. For a service already configured in Render, check its auto-deploy setting separately.

## Security note

This is a prototype. The `/api/auth/me` route currently returns demo identity/role data; it is not production authentication or authorization. Do not rely on it to protect real user or venue data.

The database, production login/permissions, and ADP workflows are still under design. Keep credentials in local environment variables or the deployment secret manager, never in source control.
