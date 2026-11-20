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

Admin endpoints require an active session and the relevant global permission. Mutations require an `Origin` matching `APP_URL`, and JSON bodies where applicable. They serialize privileged changes, recheck the actor's permissions, write an audit record in the same transaction, and reject changes that leave no active administrator with all management permissions. Users cannot modify their own privileged assignments. An actor can grant only permissions they currently hold.

`POST /api/admin/users` accepts `email`, `displayName`, and optional `roles` (an array of `{roleId, venueId}` assignments). Send a client-generated UUID in the `Idempotency-Key` header. The same key and request can retry an incomplete invitation. Use a fresh key for a separate invitation or corrected request after a definitive `400`. Reusing a key with different details or independently inviting the same account returns `409`. Initial roles use the same authorization and audit rules as standalone assignments. Venue invitations bind to the registry UUID at creation. A temporarily inactive venue can be retried with the same key after reactivation. If the venue was deleted, the operation returns `409` and releases its email reservation. Completed requests remain replayable by key, and an email can be invited again once no local account owns it.

WorkOS sends the invitation email. The invitee opens the custom invitation page, verifies their address with a WorkOS Magic Auth code, and sets their first password. PostgreSQL links the pending account after WorkOS confirms the password update and attests that the exact verified identity accepted the stored invitation. The link preserves the Admin-supplied display name and roles and activates the account. Subsequent sign-ins use the existing WorkOS identity. Archived accounts cannot accept or activate.

Use `POST /api/admin/users/{id}/invitation/resend` with a new idempotency key to resend a pending invitation or replace an expired one. Retrying that resend uses the same key. If delivery succeeds but local finalization fails, a retry uses the persisted WorkOS invitation ID and rechecks the Admin's permissions. Concurrent retries may receive 503 while delivery holds the operation lock. Retry with the same key. The one-hour retry window starts immediately before the first send or resend. Failed invitation lookups do not start it. If WorkOS delivery remains ambiguous after one hour, inspect the attempt in `user_invitation_operations` and reconcile the original delivery with WorkOS before any operator changes. Preserve the operation and verify the WorkOS invitation ID from provider records before resuming. A separate create request will not replace it. PostgreSQL enforces one local owner per case-insensitive email, including pending and archived accounts. If a sign-in claims an address during invitation finalization, the invitation returns 409 and retains its delivery ID. If the invitation claims the address first, the conflicting sign-in returns 409 without issuing a session.

`PATCH /api/admin/users/{id}` updates the local display name. `DELETE /api/admin/users/{id}` archives the local account and removes local sessions. Migration 009 requires an invitation ID for every account and enforces an inactive state for archived users. Pending users receive a WorkOS user ID when they sign in after accepting their invitation.

Role assignments use `POST /api/admin/users/{id}/roles` with `roleId` and optional `venueId`; removal uses `DELETE /api/admin/users/{id}/roles/{assignmentId}`. A user can hold different roles across venues, with one direct role per venue. Change a venue role with `PUT /api/admin/users/{id}/roles/{assignmentId}` and `{"roleId":"..."}`. The replacement is atomic and keeps the same venue. Migration 006 enforces the one-role-per-venue constraint. V1 exposes read-only role definitions and permission mappings. Authorized operators can configure them in PostgreSQL. Reactivating a user requires authority over the permissions in every retained role. Roles on suspended resources require global authority because these grants can resume after reopening. The roles and effective-permissions endpoints show access from assigned roles. Effective permissions accept an optional `venueId` query parameter. User-specific overrides and their API routes have been removed. The effective-permissions response no longer includes `override`; `inherited` and `effective` both report role-derived access.

The Users & Access directory requests `GET /api/admin/users?view=directory` with `limit`, `offset`, and optional `search`, `role`, `status`, `venueId`, and `canPublish` filters. Search matches email or display name. With a venue selected, publishing access requires an active venue and an active user with `schedules.publish` through a direct or global grant. The venue picker uses `GET /api/admin/venues?view=directory&active=true` with server-side search and paging. Both directory responses return `{items, total}`. Requests that omit the directory view continue to return arrays.

### Seed the first Admin

1. Apply the database migrations and send the first Admin an invitation from the WorkOS dashboard while the application's invitation URL still points to hosted AuthKit. The first-party page requires a local invitation record, which does not exist before this seed.
2. After acceptance, verify the invitation ID, accepted WorkOS user ID, and matching recipient email in the same WorkOS environment used by the API.
3. Run the script from the backend repository root with your private database connection and the verified values.

   ```sh
   psql "$DATABASE_URL" -X -f scripts/seed-first-admin.sql \
     -v workos_user_id='REPLACE_WITH_ACCEPTED_WORKOS_USER_ID' \
     -v workos_invitation_id='REPLACE_WITH_WORKOS_INVITATION_ID' \
     -v email='admin@example.com' \
     -v display_name='First Admin'
   ```

To get the invitation_id : 
   ```
   curl -fsSG 'https://api.workos.com/user_management/invitations' \
  -H "Authorization: Bearer $workos_api_key" \
  --data-urlencode 'email=your_email' |
  jq '.data[] | {id, email, state, accepted_user_id}'

   ```


The script creates the active local user, assigns the global Admin role, and writes an audit record in one transaction. It stops if the Admin role is already assigned or the user ID, invitation ID, or email already exists. The operator verifies the WorkOS values before running SQL. After seeding, point the invitation URL to `/invite`. The first Admin can then sign in and invite subsequent users through the Admin API.

Existing venue identifiers are Salesforce text IDs, not UUIDs. A resource permission check requires an active local user and active registry entry. It combines grants from global roles and roles assigned directly to that resource. Administrative permissions use an explicit global check. Existing event routes remain authenticated but are not yet governed by scheduling permissions.

1. Navigate the browser to `GET /auth/login` on the backend. It redirects to the hosted WorkOS AuthKit sign-in page.
2. WorkOS redirects to `GET /auth/callback`. The backend checks OAuth state, exchanges the code, verifies the access token, and links the user by their immutable WorkOS user ID.
3. On first sign-in, the backend verifies invitation acceptance before linking the local account. On later sign-ins it uses the linked WorkOS ID. It establishes a session and redirects to `APP_URL`.
4. Completing invitation setup activates the local account.
5. The frontend can call `GET /auth/me` with credentials to retrieve the active local identity.

### Current-user authorization

Both endpoints use the active session and read current PostgreSQL assignments and permission mappings. They require no Admin permission and return `Cache-Control: no-store`.

| Endpoint | Response |
| --- | --- |
| `GET /auth/access` | `global` contains directly assigned global roles and permissions. `venues` is keyed by Salesforce venue ID. Each venue contains all directly assigned role names and the combined direct and inherited global permission keys. |
| `GET /api/me/venues` | A list of `{id, name, roles}` ordered by name and ID. Roles are direct venue assignments. A venue visible only through the global Admin role has an empty `roles` array. |

The predefined global Admin role can discover all active venues. Other users see only active venues where they have a direct role assignment. Global roles still contribute effective permissions at visible venues. These endpoints describe current access for the signed-in user and do not change scheduling route enforcement. Use the global scope for global Admin operations.

### Invitation-only WorkOS setup

In each WorkOS environment, turn off public **Sign up** under **Authentication**. Keep **Email + Password**, WorkOS invitation delivery, and email verification enabled. Under **Applications → your application → Redirects**, set **User invitation URL** to the frontend's `https://<frontend-host>/invite`. For local Vite development, use `http://localhost:5173/invite` if Vite is running on port 5173. WorkOS supplies `invitation_token` in the email link. Send a fresh invitation after updating the setting. The invitation URL is separate from the OAuth callback and password reset URL. [WorkOS invitation delivery and URL setup](https://workos.com/docs/authkit/custom-emails)

Enable **Magic Auth** and its default email delivery in the WorkOS application. Apply migration 010 before running the new backend. The invitation page loads the read-only recipient profile, then requests a six-digit code through `POST /auth/invite/send-code`. `POST /auth/invite/verify` authenticates the code with WorkOS and checks the exact invitation and accepted user ID. The browser receives a ten-minute onboarding cookie backed by a hashed session ID in PostgreSQL. `POST /auth/invite/password` sets the password on that verified user through WorkOS, links the local account, and consumes the session. The user becomes active and can sign in after setup. No password, code, or WorkOS authentication token is persisted.

Refreshing resumes from the onboarding cookies. If the WorkOS invitation was accepted but verification could not be recorded locally, reopen the original link and request another code. The backend verifies the original acceptance again. A definite password policy rejection can be retried. An ambiguous password update is reserved against replay. If the password worked, a later password sign-in reconciles the exact reserved identity after a brief grace period. If WorkOS never applied the password or the original invitation changed, an administrator must review the account before retrying setup.

**Staging verification.** With an approved test mailbox, send a new invitation from Users & Access. Open `/invite`, confirm the saved name and email, click Continue, and enter the WorkOS code. Refresh the page before choosing a password to confirm the session resumes. Set a strong password and confirm the redirect to `/login`. Check in WorkOS that the same invitation was accepted by the verified user. Confirm restricted routes reject that user until an Admin activates the local account, then confirm password sign-in grants the assigned access.

Use the frontend proxy or same-site frontend and backend hosts, and match the frontend origin exactly in `APP_URL`. Configure hosting and observability to omit invitation URL query strings, request bodies and cookies from logs. The app removes the token from the address bar on arrival, but the initial email link necessarily reaches the hosting layer with the token in its query string.

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

Invited users become active after completing invitation setup. An authorized Admin can reactivate a subsequently deactivated account with `POST /api/admin/users/{id}/activate`. A protected request returns `401` without a valid session and `403` for an inactive local account.

## API routes

| Route | Purpose | Access |
| --- | --- | --- |
| `GET /healthz` | Health check | Public |
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

Migrations 001 and 002 create the event and authentication tables. Version 003 adds event-sync tracking and matches `dev`. Dev uses versions 004 and 005 for labor goals and venue metadata. Admin migrations 006–010 add RBAC and audit tables, administrative user profiles, security guards, invitations, and onboarding sessions. The version 002 down migration deletes all local users and sessions. Version 008 blocks rollback and protects the audit table with a database dependency.

Apply the combined migration sequence to a fresh development database or a database with the dev migrations through 005 and an empty `users` table. Migration 009 requires an invitation ID for every user. Review invitation linkage before upgrading a database that already contains users. Recreate disposable local databases that applied the earlier Admin numbering.

Migration 006 preserves audit history and makes assignment IDs, users, roles, and venues immutable through SQL UPDATE. Use the protected Admin assignment removal and creation endpoints to change access. Removing an assignment revokes its grants. Grants from other assignments still apply.

Migration 008 is intentionally irreversible. Its down migration raises an error, and the 006 down script is also blocked by a view dependency on the security history tables. Do not drop that view, use CASCADE, or force the migration version backwards to bypass protection. A database owner can bypass database safeguards and must follow operator access controls.

If rollback of migration 008 is attempted, golang-migrate marks version 7 dirty before the guard raises an error. The version 8 schema remains intact. Stop migrations and inspect the schema and migration version. After confirming the version 008 guard view, immutable-assignment trigger, and audit data are intact, an authorized operator can restore clean version metadata with golang-migrate's `force 8` and resume forward migrations. For any broader recovery, take a verified backup and use a separately reviewed restore plan that preserves all audit records and assignments. Avoid the destructive historical down scripts.


### Resource-scoped RBAC

Migration 004 creates the final resource registry, role assignment, and audit tables. It backfills the registry from existing venues without changing venue or event rows. The role and assignment `assignment_type` must match. A global assignment has no resource. A scoped assignment references a registry UUID of the same type. Different resource types can use the same external ID.

Call `HasGlobalPermission` for global operations and `HasResourcePermission` with a registry UUID for scoped operations. Applications provide their authenticated-user and resource resolvers to the middleware. The WedgeWood Admin API still accepts Salesforce `venueId` values and returns its existing venue fields. The permission breakdown returns 404 for unknown venues. For inactive venues it returns false for scoped permissions, while global management permissions are evaluated separately.

The venue-table trigger mirrors inserts and status changes into the registry. Migration 006 sets legacy NULL activity to true and makes the column non-nullable. A suspended venue reopens only when its status is explicitly set to true. A false status suspends access, including global grants when evaluated against that venue, and blocks new assignments. Administrators can inspect and revoke existing assignments while the venue is suspended. Reopening the venue restores access from remaining assignments and keeps the same UUID. The existing Salesforce upserts preserve venue activity on conflict. An external ID reused without a recorded delete still represents the same venue. A hard delete audits direct grants and cascades their removal. Recreating that venue creates a fresh registry UUID.

Apply migrations through 010 before starting the matching release.
