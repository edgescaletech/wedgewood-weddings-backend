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

6. Set `APP_URL` to your frontend's origin. The application frontend is maintained separately; it can use the custom password endpoint or the hosted WorkOS sign-in flow. If testing without a frontend, you can use `http://localhost:8080` as `APP_URL`, but the backend has no page at `/`.

## Authentication flow

1. Navigate the browser to `GET /auth/login` on the backend. It redirects to the hosted WorkOS AuthKit sign-in page.
2. WorkOS redirects to `GET /auth/callback`. The backend checks OAuth state, exchanges the code, verifies the access token, and links the user by their immutable WorkOS user ID.
3. The backend creates an active local user if one does not already exist, establishes a session, and redirects the browser to `APP_URL`. Existing users retain their current active status.
4. Inactive existing users must be activated by a privately authorized operator. See [Activate a user](#activate-a-user).
5. The frontend can call `GET /auth/me` with credentials to retrieve the active local identity.

### Temporary signup

Open `/signup/` on the frontend to create an account. The page sends email and password to `POST /auth/signup`, which creates an unverified WorkOS user. The password is handled by WorkOS and is not saved in PostgreSQL. After signup, sign in using the existing login page; WorkOS can require email verification, and the local user is created on the first successful sign-in.

To disable signup later, remove the `/auth/signup` route in `cmd/server/main.go` and deploy the backend; deleting the frontend page alone does not disable the API. Then remove the signup handler, rate limiter, tests and OpenAPI entry, the frontend `signup/` directory, and its entry in `vite.config.js`.

Signup is limited to 30 POST requests per 10 minutes per server process using an in-memory counter. A full window returns `429` with `Retry-After`. The counter resets on restart and is not shared between instances. It does not distinguish clients, so one busy or abusive client can temporarily block everyone on that instance. Use a trusted edge per-IP rate limiter for public deployment.

New users are temporarily created active by the `INSERT INTO users` statement in `internal/auth/login.go`. To restore inactive-by-default behavior, change its `is_active` value from `TRUE` to `FALSE` (or omit `is_active` and its value if the database column default is false). This affects future users only and does not deactivate existing accounts. No migration is needed for the explicit value; the existing migration's default remains false.

### Custom sign-in, verification, and password recovery

The custom frontend can submit JSON to the following endpoints. All requests must use `POST`, `Content-Type: application/json`, and an `Origin` matching `APP_URL`; browser requests must include credentials.

1. `POST /auth/password` with `{"email":"...","password":"..."}` returns `204` with a session cookie on success. If WorkOS requires email verification, it returns `202` and `{"status":"email_verification_required"}` with a short-lived encrypted HttpOnly challenge cookie.
2. When verification is required, `POST /auth/verify-email` with `{"code":"..."}` and the challenge cookie. A successful response returns `204` and creates the usual session. Incorrect or expired challenges do not grant access. An expired challenge requires signing in again.
3. `POST /auth/forgot-password` with `{"email":"..."}` requests a WorkOS reset email. The `204` response does not reveal whether the account exists.
4. The frontend reset page reads the one-time `token` from the email link and sends it to `POST /auth/reset-password` as `{"token":"...","newPassword":"..."}`. A successful response returns `204`. WorkOS revokes its sessions and the backend deletes local sessions for that user; the user must sign in again.

In WorkOS, enable the email verification and password reset emails, and configure the password reset URL under **Applications → your application → Redirects** to point to the frontend reset page. That page captures the `token` query parameter and removes it from the address bar after reading it. Do not log or send tokens to analytics. WorkOS applies its password policy and sends emails; this backend does not store passwords or send emails itself. WorkOS MFA, SSO, and Radar challenges are not implemented in the custom flow. Configure appropriate per-IP abuse protection before public deployment.

If WorkOS resets the password but PostgreSQL session cleanup fails, the endpoint returns `503` explaining that the password changed. Local sessions may remain usable until their access tokens expire; an operator must remove that user's local sessions. This cross-service failure requires operational attention.

For same-site, cross-origin browser requests, configure `APP_URL` to the exact frontend origin and send requests with credentials, for example:

```js
const response = await fetch('http://localhost:8080/auth/me', {
  credentials: 'include',
});
```

Session cookies use `SameSite=Lax`, so this setup does not support cross-site frontend API requests. For logout, send `POST /auth/logout` with credentials; the request's `Origin` must match `APP_URL`. Postman has a separate cookie store from the browser.

## Activate a user

New users are currently active by default. Existing inactive users cannot access protected endpoints. An authorized operator with private database access should verify the complete WorkOS ID before activating an account:

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
| `POST /auth/signup` | Temporarily create a WorkOS password account | Public; matching `Origin` required |
| `POST /auth/password` | Sign in with email and password | Public; matching `Origin` required |
| `POST /auth/verify-email` | Complete an email verification challenge | Public; challenge cookie and matching `Origin` required |
| `POST /auth/forgot-password` | Request a reset email | Public; matching `Origin` required |
| `POST /auth/reset-password` | Reset a password using a one-time token | Public; matching `Origin` required |
| `GET /auth/login` | Redirect to WorkOS AuthKit | Public |
| `GET /auth/callback` | Complete the WorkOS sign-in flow | Public; validates OAuth state |
| `GET /auth/me` | Current local user | Active session required |
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

**Authorization and RBAC are not implemented.** Authenticated active users can request event data for every venue, including by changing the `venueId` filter. Do not use this prototype to protect sensitive production data until authorization is implemented. ADP and scheduling workflows remain under design.

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

Migration version 1 creates event tables; version 2 creates authentication tables. The version 2 down migration **deletes all local users and sessions**. Back up the database before rolling back, and only use it when those tables were introduced by version 2.

Older databases that ran the authentication branch's original version 1 instead of `dev`'s version 1 must apply `internal/migrations/001_phase1_tables.up.sql` manually before upgrading. Those databases may already have user tables that predate version 2, so do not run the version 2 down migration on them without a reviewed recovery plan.

`CREATE TABLE IF NOT EXISTS` does not repair an existing table with a different definition. Inspect unexpected schemas before applying migrations. The version 1 down migration drops event-related tables and their data; do not use it against a database containing data you need.
