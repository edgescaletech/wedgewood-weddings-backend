# Backend testing

Run commands from the backend repository. Use the Go toolchain specified in [go.mod](go.mod), Make, and PostgreSQL for integration tests. Docker is one way to provide the local test database.

## Commands

| Command | What runs | Dependencies |
| --- | --- | --- |
| `make test` | Standard Go test suite with normal Go caching | Integration tests skip unless `RBAC_TEST_DATABASE_URL` is exported. The HTTP smoke test skips unless `SMOKE_BASE_URL` is exported. |
| `make test-smoke` | Six read-only checks against a running Go API | Running local or staging API |
| `make test-regression` | Uncached Go tests, including PostgreSQL integration tests | Disposable PostgreSQL with permission to create schemas |
| `make test-functional` | Smoke, then regression | Running API and disposable PostgreSQL |
| `go vet ./...` and `go build ./...` | Static checks and compilation | Go toolchain |

The functional targets read unquoted assignments from the Git-ignored `.env.test`. Create it once from [the example](.env.test.example).

```sh
cp .env.test.example .env.test
```

- `SMOKE_BASE_URL` is the API origin only. It defaults to `http://localhost:8080`.
- `RBAC_TEST_DATABASE_URL` is the integration database connection URL. The regression target fails if it is empty. Use a dedicated local or disposable database. Tests apply migrations within uniquely named temporary schemas and clean them up when they finish.
- `.env` configures the running application. `.env.test` configures the test runner. The regression database can be separate from the API database.

## Local setup

The example DSN matches this loopback-only Docker setup. Use a PostgreSQL image compatible with your deployment. The username and password here are for the disposable local container.

```sh
docker run --detach --name wedgewood-test-postgres \
  --publish 127.0.0.1:55433:5432 \
  --env POSTGRES_DB=wedgewood_test \
  --env POSTGRES_USER=wedgewood_test \
  --env POSTGRES_PASSWORD=wedgewood_local_test \
  postgres:latest

docker exec wedgewood-test-postgres pg_isready -U wedgewood_test -d wedgewood_test
make test-regression
```

For an existing stopped container, run `docker start wedgewood-test-postgres` instead of creating another one. Regression does not require a running API or live provider credentials.

For a local smoke run, configure the Go API and migrate its application database using [Run locally](README.md#run-locally). Start it with `make run` in one terminal and run the following in another.

```sh
make test-functional
```

Use `make test-smoke` when you only want the HTTP checks.

## Staging

Point the smoke URL at the deployed staging API. Keep `RBAC_TEST_DATABASE_URL` pointed at the disposable test database. The deployed API database does not need to be exposed to the runner.

```sh
# Run both suites with a one-run smoke URL override.
make test-functional SMOKE_BASE_URL=https://wedgewood-dev-api.onrender.com

# Run only the smoke checks against the deployment.
make test-smoke SMOKE_BASE_URL=https://wedgewood-dev-api.onrender.com
```

Alternatively, save the staging origin as `SMOKE_BASE_URL` in `.env.test` and run `make test-functional`. The smoke checks send no credentials and follow no redirects. They require the deployment to be reachable. The first health request has a 75-second startup allowance. Each protected request has a five-second timeout.

## Smoke coverage

[smoke_test.go](cmd/server/smoke_test.go) makes these HTTP requests and checks both status and JSON.

| GET endpoint | Expected result |
| --- | --- |
| `/healthz` | `200` and `{"status":"ok"}` |
| `/auth/me` | `401` and `{"error":"authentication required"}` |
| `/auth/access` | `401` and the same authentication error |
| `/api/me/venues` | `401` and the same authentication error |
| `/api/events` | `401` and the same authentication error |
| `/api/admin/users` | `401` and the same authentication error |

## Regression coverage

### Server, events, and jobs

| Test file | What it verifies |
| --- | --- |
| [auth_routes_test.go](cmd/server/auth_routes_test.go) | Route registration and access boundaries for public and protected handlers. |
| [events_test.go](cmd/server/events_test.go) | Week bounds, flexible date parsing, health JSON, invalid request handling, venue authorization, empty event results for users with no venues, and the sync handler's date validation and accepted response. |
| [events_sync_integration_test.go](cmd/server/events_sync_integration_test.go) | With PostgreSQL and a Salesforce HTTP double, verifies event sync, cached reads, and Salesforce fallback. |
| [venue_access_test.go](cmd/server/venue_access_test.go) | PostgreSQL event isolation, Salesforce list filtering, permitted and denied Salesforce event detail reads, and generic `404` responses for malformed IDs without contacting Salesforce. |
| [event_sync_test.go](internal/jobs/event_sync_test.go) | Duplicate work for the same week and the worker's concurrency limit. |
| [labor_goal_service_test.go](internal/services/labor_goal_service_test.go) | Existing labor calculation unit tests run as part of the full Go suite. |

### Authentication

| Test file | What it verifies |
| --- | --- |
| [auth_test.go](internal/auth/auth_test.go) | Login state, invalid state handling, missing or inactive sessions, token verification, callback account lookup, and expired access token refresh. |
| [password_test.go](internal/auth/password_test.go) | Password login rejects an untrusted origin and creates a session on success. |
| [session_resilience_test.go](internal/auth/session_resilience_test.go) | Logout and WorkOS revocation, refresh failures, revoked tokens, preserving rotated tokens during signing-key outages, and retryable verification failures. |
| [recovery_test.go](internal/auth/recovery_test.go) | Email verification challenge and completion, origin and expiry checks, privacy for password reset requests, local session cleanup, and reset responses `410`, `422`, `429`, and `503`. |
| [workos_password_error_test.go](internal/auth/workos_password_error_test.go) | Maps WorkOS password errors to application responses. |
| [invitation_test.go](internal/auth/invitation_test.go) | Requires the exact accepted WorkOS identity and rejects conflicting email ownership. |
| [workos_invite_test.go](internal/auth/workos_invite_test.go) | Verifies the WorkOS request sequence for Magic Auth and password setup using an HTTP double. |
| [invite_acceptance_test.go](internal/auth/invite_acceptance_test.go) | Input validation and PostgreSQL onboarding. Covers success, expired or revoked invitations, identity mismatch, invalid or expired verification, password update failures, recovery after local failures, and concurrent completion. |

### Authorization and Admin

| Test file | What it verifies |
| --- | --- |
| [authorization_test.go](internal/authorization/authorization_test.go) | Database-backed permission resolution and rejection of missing identity. |
| [integration_test.go](internal/authorization/integration_test.go) | PostgreSQL-backed role grants, venue scope, and user-specific permission overrides. |
| [read_test.go](internal/admin/read_test.go) | Admin authentication requirements and invalid pagination. |
| [provision_test.go](internal/admin/provision_test.go) | WorkOS Admin invitation request against an HTTP double. |
| [security_migration_test.go](internal/admin/security_migration_test.go) | Security constraints in the PostgreSQL migrations. |
| [integration_test.go](internal/admin/integration_test.go) | Concurrent Admin changes, current-user access, directory filtering, invitation idempotency and resend safeguards, scoped grants and denials, and deactivated-account restrictions. |

Go regression tests use SQL mocks, local HTTP doubles, or temporary PostgreSQL schemas as appropriate. Live sign-in and venue-switching scenarios belong to the frontend Playwright suite.

## Targeted runs and troubleshooting

```sh
go test -count=1 -v ./internal/auth -run '^TestResetPasswordWorkOSFailureResponses$'
go test -count=1 -v ./cmd/server -run 'TestEventDetailSkipsSalesforceForMalformedIDs|TestSalesforceEventDetailFallbackEnforcesVenueAccess'
```

- Use `-v` on targeted Go runs to see which cases were skipped. `[no test files]` is informational.
- If smoke fails with a connection error, check the origin and confirm the API is running. A failing smoke prerequisite stops the combined Make target. Run `make test-regression` to check PostgreSQL independently.
- If regression cannot connect, check that Docker is running, the host port matches the DSN, and the test user can create schemas.

## Verification

`make test-functional` passed all six smoke checks against the dev deployment and the full Go regression suite against the local Docker PostgreSQL database.
