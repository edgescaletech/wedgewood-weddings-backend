# Wedgewood Weddings Backend

Go HTTP API for the venue scheduling prototype. Salesforce provides event data; WorkOS AuthKit handles sign-in; PostgreSQL stores local users and authentication sessions.

## Requirements

- Go 1.25.3 or newer, as specified in `go.mod`.
- PostgreSQL accessible to the API and migration command.
- A Salesforce connected app or a Salesforce access token.
- A WorkOS environment with AuthKit configured.

## Run locally

1. Copy `.env.example` to `.env` and fill in the Salesforce, PostgreSQL, and WorkOS values described below. The server loads `.env` from its working directory; existing environment variables take precedence. **Never commit `.env` or real credentials.**

   ```sh
   cp .env.example .env
   ```

2. Configure WorkOS and generate the cookie password as described in [WorkOS setup](#workos-setup).

3. Apply the database migrations from the repository root. The migration command reads `DATABASE_URL` from the shell; it does **not** load `.env` itself. For example, for the local Docker database used in development:

   ```sh
   export DATABASE_URL='postgresql://postgres:postgres@localhost:5432/postgres?sslmode=disable'
   go run ./cmd/migrate
   ```

   Use your own database credentials and SSL settings. The example disables SSL only for local development. The command applies pending **up** migrations from `internal/migrations` and records their versions in PostgreSQL.

4. Start the API:

   ```sh
   make run
   ```

   It listens on `http://localhost:8080` by default. Set `PORT` to change the port. The server needs access to PostgreSQL at startup; Salesforce credentials are needed to fetch event data.

## Configuration

| Variable | Purpose |
| --- | --- |
| `SF_MY_DOMAIN_URL` | Salesforce My Domain URL; required at startup. |
| `SF_CLIENT_ID`, `SF_CLIENT_SECRET` | Salesforce connected-app credentials for OAuth. |
| `SF_ACCESS_TOKEN` | Optional direct Salesforce access token for development. |
| `DATABASE_URL` | PostgreSQL connection string for users, sessions, and migrations. |
| `WORKOS_API_KEY` | WorkOS server-side API key. |
| `WORKOS_CLIENT_ID` | WorkOS client ID for this environment. |
| `WORKOS_REDIRECT_URI` | Callback registered in WorkOS, e.g. `http://localhost:8080/auth/callback`. |
| `WORKOS_ISSUER` | Exact issuer expected in the WorkOS session access token, including any path or trailing slash. |
| `WORKOS_COOKIE_PASSWORD` | High-entropy secret of at least 32 bytes used to encrypt authentication state and session data. |
| `APP_URL` | Browser frontend origin and post-login redirect, e.g. `http://localhost:3000`. It must match the browser's `Origin` header for logout and credentialed CORS. |
| `COOKIE_SECURE` | Defaults to `true`; set `false` only for local HTTP development. Use HTTPS and secure cookies in production. |
| `PORT` | Optional HTTP server port; defaults to `8080`. |

## WorkOS setup

1. In the WorkOS dashboard, configure AuthKit and enable email/password sign-in. Create or invite a test user.
2. Register `WORKOS_REDIRECT_URI` as an allowed redirect URI. For a local server on port 8080, use `http://localhost:8080/auth/callback`.
3. Set the WorkOS API key and client ID in `.env`. The API key stays on the backend.
4. Set `WORKOS_ISSUER` to the **exact** `iss` claim used by your WorkOS session access tokens. WorkOS documents an issuer value in the `issuer` field of `https://api.workos.com/user_management/<WORKOS_CLIENT_ID>/.well-known/openid-configuration`. Check the value for your environment, especially if it uses a custom authentication domain.
5. Generate a cookie password and place the output in `.env`:

   ```sh
   openssl rand -hex 32
   ```

6. Set `APP_URL` to your frontend's origin. The backend currently uses the WorkOS-hosted sign-in page; the application frontend is maintained separately. If testing without a frontend, you can use `http://localhost:8080` as `APP_URL`, but the backend has no page at `/`.

## Authentication flow

1. Navigate the browser to `GET /auth/login` on the backend. It redirects to the hosted WorkOS AuthKit sign-in page.
2. WorkOS redirects to `GET /auth/callback`. The backend checks OAuth state, exchanges the code, verifies the access token, and links the user by their immutable WorkOS user ID.
3. The backend creates an inactive local user if one does not already exist, establishes a session, and redirects the browser to `APP_URL`.
4. A privately authorized operator must activate a new local user before protected endpoints can be used. See [Activate a user](#activate-a-user).
5. The frontend can call `GET /auth/me` with credentials to retrieve the active local identity. `GET /api/auth/me` is an alias.

For same-site, cross-origin browser requests, configure `APP_URL` to the exact frontend origin and send requests with credentials, for example:

```js
const response = await fetch('http://localhost:8080/auth/me', {
  credentials: 'include',
});
```

Session cookies use `SameSite=Lax`, so this setup does not support cross-site frontend API requests. For logout, send `POST /auth/logout` with credentials; the request's `Origin` must match `APP_URL`. Postman has a separate cookie store from the browser.

## Activate a user

New users have `is_active = false` and cannot access protected endpoints. An authorized operator with private database access should verify the complete WorkOS ID before running:

```sql
SELECT workos_id, email, is_active FROM users;

UPDATE users
SET is_active = true, updated_at = now()
WHERE workos_id = 'user_verified_workos_id'
RETURNING workos_id, email, is_active;
```

Copy the complete ID, not a truncated value from a table display. There is no public activation endpoint. A protected request returns `401` without a valid session and `403` for an inactive local account.

## API routes

| Route | Purpose | Access |
| --- | --- | --- |
| `GET /healthz` | Health check | Public |
| `GET /auth/login` | Redirect to WorkOS AuthKit | Public |
| `GET /auth/callback` | Complete the WorkOS sign-in flow | Public; validates OAuth state |
| `GET /auth/me`, `GET /api/auth/me` | Current local user | Active session required |
| `POST /auth/logout` | Remove local session and attempt WorkOS session revocation | Requires session cookie and matching `Origin` |
| `GET /api/events?week=YYYY-MM-DD` | Event data for a week | Active session required |
| `GET /api/events/{id}` | Event details | Active session required |
| `GET /docs` | Swagger UI | Public |
| `GET /openapi.yaml` | API specification | Public |

For events, the optional `venueId` query parameter filters results and `refresh=true` requests fresh data from Salesforce. The `week` date identifies a week; results cover Monday through Sunday.

## Sessions and access

The browser holds an opaque `HttpOnly`, `SameSite=Lax` session cookie. WorkOS session data is encrypted in PostgreSQL, and access tokens are verified against WorkOS signing keys. Expired access tokens can be refreshed; the local session has a 24-hour expiry. Changing `WORKOS_COOKIE_PASSWORD` invalidates existing encrypted sessions. Local account deactivation takes effect on the next protected request.

Logout removes the local session and attempts to revoke the WorkOS session, including when the access token has expired. A `503` indicates that the local session ended but remote revocation failed or could not be attempted; the WorkOS session may need to be revoked in the dashboard.

When WorkOS refresh or signing-key retrieval is temporarily unavailable, protected requests return `503` and preserve the local session for a later retry. An explicit `invalid_grant` refresh response returns `401`. Rotated refresh tokens are saved before the replacement access token is verified, but access is denied until verification succeeds.

**Authorization and RBAC are not implemented.** All active users can currently request event data for every venue, including by changing the `venueId` filter. Do not use this prototype to protect sensitive production data until authorization is implemented. ADP and scheduling workflows remain under design.

## Salesforce Pub/Sub subscriber

The subscriber runs separately from the HTTP server. Configure the required Salesforce environment variables, then run:

```sh
go run ./cmd/pubsub/
```

It listens for Salesforce change events and prints them; it does not invalidate the HTTP event cache.

## Development checks

```sh
gofmt -w ./cmd ./internal
go test ./...
go vet ./...
go build ./...
```

Unit tests mock WorkOS and database calls. A real end-to-end test requires configured PostgreSQL, WorkOS credentials, and a registered redirect URI. Keep credentials in `.env` locally or in your deployment secret manager, never in source control.

## Database-backed event synchronization

Weekly and individual event requests use the PostgreSQL event cache when available and fall back to Salesforce. The background Salesforce Pub/Sub subscriber starts with the API and updates the database. Apply migrations before starting the service. Render auto-deploy is disabled; deploy only after migrations succeed.

Migration version 2 reconciles databases that previously applied either branch's version 1 migration. Back up the database before applying it and do not reset migration versions manually. Migration 2 is **forward-only**: its schema may include tables that predate the merge, so automatically dropping those tables could destroy existing data. `make migration.down` and direct CLI rollback are blocked by the version 2 down SQL; golang-migrate marks a failed rollback as dirty. If either rollback command is attempted, stop and inspect the database and migration state before recovery. Restore a verified backup if a rollback is required.

`CREATE TABLE IF NOT EXISTS` makes reconciliation safe for the expected version 1 schemas from both branches, but it does not repair an existing table with a different definition. Inspect unexpected schemas before applying migrations. The version 1 down migration drops event-related tables and their data; do not use it against a database containing data you need.
