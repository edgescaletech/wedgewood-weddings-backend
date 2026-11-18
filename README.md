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

### Admin management status

Admin endpoints require an active session and the relevant global permission. Mutations require an `Origin` matching `APP_URL`, and JSON bodies where applicable. They serialize privileged changes, recheck the actor's permissions, write an audit record in the same transaction, and reject changes that leave no active administrator with all management permissions. Users cannot modify their own privileged assignments or overrides. An actor can grant only permissions they currently hold.

`POST /api/admin/users` accepts `email`, `displayName`, and `password`, creates an unverified WorkOS identity with that password, and creates an inactive local user. WorkOS stores the password; PostgreSQL stores the linked identity and application details. The full display name is also sent as the WorkOS first name because the installed SDK exposes separate first and last name fields. No invitation is sent. If WorkOS creation succeeds but the local transaction fails, a WorkOS identity may remain without a local record; an operator must reconcile it in WorkOS before retrying. `PATCH /api/admin/users/{id}` updates the local `displayName`; WorkOS controls identity details. `DELETE` archives the account locally and removes its local sessions; it does not delete the WorkOS identity. An archived account cannot be reactivated through the API.

Role assignments use `POST /api/admin/users/{id}/roles` with `roleId` and optional `venueId`; removal uses `DELETE /api/admin/users/{id}/roles/{assignmentId}`. Replace a role's mapping with `PUT /api/admin/roles/{id}/permissions` and `{"permissions":["schedules.view"]}`. Set an override with `PUT /api/admin/users/{id}/permissions/overrides` and `{"permission":"schedules.approve","effect":"allow","venueId":"SALESFORCE_VENUE_ID"}`; use `null` for global scope. Remove an override with `DELETE /api/admin/users/{id}/permissions/overrides/{overrideId}`. The corresponding GET endpoints return roles, overrides and effective permissions; effective permissions accept an optional `venueId` query parameter. Removing the last role for a scope also removes its overrides and records them in the audit entry.

After applying the Admin migrations through 006, an operator can bootstrap one administrator. The user must already have a local account from WorkOS sign-in, a verified WorkOS email, and an active membership in the configured WorkOS organization with the configured bootstrap role. Set `WORKOS_API_KEY`, `DATABASE_URL`, `BOOTSTRAP_WORKOS_USER_ID`, `BOOTSTRAP_WORKOS_ORGANIZATION_ID`, and `BOOTSTRAP_WORKOS_ROLE_SLUG` in the operator's environment, then run `go run ./cmd/bootstrap-admin`. The command verifies WorkOS directly, activates that existing local user, assigns the global Admin role, writes an audit record, and records a one-time marker. Do not expose the command or credentials to users. Following bootstrap, permission checks use PostgreSQL.

Existing venue identifiers are Salesforce text IDs, not UUIDs. A permission check requires an active local user and matching role scope; explicit denies override allows, which override inherited role permissions. A venue role grants access only to its assigned venue, while administrative permissions require a global role. Existing event routes remain authenticated but are not yet governed by scheduling permissions.

1. Navigate the browser to `GET /auth/login` on the backend. It redirects to the hosted WorkOS AuthKit sign-in page.
2. WorkOS redirects to `GET /auth/callback`. The backend checks OAuth state, exchanges the code, verifies the access token, and links the user by their immutable WorkOS user ID.
3. The backend creates an inactive local user if one does not already exist, establishes a session, and redirects the browser to `APP_URL`. Existing users retain their current active status.
4. Inactive existing users must be activated by a privately authorized operator. See [Activate a user](#activate-a-user).
5. The frontend can call `GET /auth/me` with credentials to retrieve the active local identity.

### Temporary signup

Open `/signup/` on the frontend to create an account. The page sends email and password to `POST /auth/signup`, which creates an unverified WorkOS user. The password is handled by WorkOS and is not saved in PostgreSQL. After signup, sign in using the existing login page; WorkOS can require email verification, and the local user is created on the first successful sign-in.

To disable signup later, remove the `/auth/signup` route in `cmd/server/main.go` and deploy the backend; deleting the frontend page alone does not disable the API. Then remove the signup handler, rate limiter, tests and OpenAPI entry, the frontend `signup/` directory, and its entry in `vite.config.js`.

Signup is limited to 15 POST requests per 10 minutes per server process using an in-memory counter. A full window returns `429` with `Retry-After`. The counter resets on restart and is not shared between instances. It does not distinguish clients, so one busy or abusive client can temporarily block everyone on that instance. Use a trusted edge per-IP rate limiter for public deployment.

New local users are created inactive. An authorized administrator must activate them and assign the appropriate role and venue before they receive Admin API access. Existing users retain their current active status.

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

New users are inactive by default and cannot access protected endpoints. An administrator can activate users through the protected Admin API. An authorized operator with private database access can also verify the complete WorkOS ID before activating an account:

```sql
SELECT workos_id, email, is_active FROM users;

UPDATE users
SET is_active = true, updated_at = now()
WHERE workos_id = 'user_verified_workos_id'
RETURNING workos_id, email, is_active;
```

Copy the complete ID, not a truncated value from a table display. Activation requires an authorized Admin API session or private database access. A protected request returns `401` without a valid session and `403` for an inactive local account.

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

Set `RBAC_TEST_DATABASE_URL` to a disposable PostgreSQL database to run the optional integration tests. They create uniquely named temporary schemas, apply migrations there, and remove those schemas after the tests.

## Database-backed event synchronization

Weekly and individual event requests use the PostgreSQL event cache when available and fall back to Salesforce. The background Salesforce Pub/Sub subscriber starts with the API and updates the database. Apply migrations before starting the service. Render auto-deploy is disabled; deploy only after migrations succeed.

Migrations 001 and 002 create the event and authentication tables. Version 003 is reserved for the event-sync tracking migration on `dev`. Admin migrations 004, 005, and 006 add RBAC and audit tables, local display names and archival state, and security guards, respectively. The version 002 down migration deletes all local users and sessions, and the version 004 down migration is destructive. Version 006 blocks rollback and protects the bootstrap and audit tables with a database dependency.

The renumbered migrations are for fresh databases and databases that have only applied the `dev` migration history. If a database already applied the earlier authorization branch's `003_admin`, `004_user_admin`, or `005_security_guards`, do not run the renumbered sequence on it. Its version numbers describe a different schema. Back up and reconcile the actual schema and migration metadata, including the event-sync changes, before upgrading a database with data to retain. For disposable local databases, start with a fresh database and run the merged sequence.

Older databases that ran the authentication branch's original version 1 instead of `dev`'s version 1 must apply `internal/migrations/001_phase1_tables.up.sql` manually before upgrading. Those databases may already have user tables that predate version 2, so do not run the version 2 down migration on them without a reviewed recovery plan.

`CREATE TABLE IF NOT EXISTS` does not repair an existing table with a different definition. Inspect unexpected schemas before applying migrations. The version 1 down migration drops event-related tables and their data; do not use it against a database containing data you need.


Migration 006 preserves permanent bootstrap and audit history and makes assignment IDs, users, roles, and venues immutable through SQL UPDATE. Use the protected Admin assignment removal and creation endpoints to change access. Removal still checks effective permissions after override cleanup; changes that would restore a denied permission are rejected and rolled back.

Migration 006 is intentionally irreversible. Roll back application code while retaining the database schema. Its down migration raises an error; the historical 004 down script is also blocked by a view dependency on the security history tables. Do not drop that view, use CASCADE, or force the migration version backwards to bypass protection. A database owner can bypass database safeguards and must follow operator access controls.

If rollback of migration 006 is attempted, golang-migrate records its target version (5) as dirty before the guard raises an error; the version 6 schema remains intact. Stop migrations and inspect the schema and migration version. After confirming 006's guard view, immutable-assignment trigger, bootstrap marker, and audit data are intact, an authorized operator can mark **version 6** clean with golang-migrate's `force 6` and resume forward migrations. This is version metadata recovery, not rollback. For any broader recovery, take a verified backup and use a separately reviewed restore plan that preserves the permanent bootstrap marker, all audit records, and assignments/overrides; do not run destructive historical down scripts or reset bootstrap.
