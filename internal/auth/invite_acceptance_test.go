package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	workos "github.com/workos/workos-go/v7"
)

const (
	inviteToken           = "test-invitation-token"
	inviteID              = "invitation_test"
	inviteEmail           = "invited@example.test"
	inviteUser            = "user_invited"
	invitePass            = "LongExamplePassword42!"
	invitePasswordRequest = `{"invitationId":"` + inviteID + `","password":"` + invitePass + `","confirmPassword":"` + invitePass + `"}`
)

type inviteAcceptanceMock struct {
	fakeProvider
	invite           workos.UserInvite
	accepted         bool
	sendCalls        int
	passwordCalls    atomic.Int32
	passwordError    error
	sendError        error
	cancelLookup     context.CancelFunc
	verifyError      error
	wrongEmail       bool
	wrongAcceptedID  bool
	preexistingUser  bool
	started, release chan struct{}
}

func (p *inviteAcceptanceMock) FindInvite(_ context.Context, token string) (*workos.UserInvite, error) {
	if token != inviteToken {
		return nil, &workos.APIError{StatusCode: 404}
	}
	return p.GetInvitation(context.Background(), p.invite.ID)
}

func (p *inviteAcceptanceMock) GetInvitation(ctx context.Context, id string) (*workos.UserInvite, error) {
	if p.cancelLookup != nil {
		p.cancelLookup()
		return nil, ctx.Err()
	}
	if id != inviteID {
		return nil, errors.New("wrong invitation")
	}
	invite := p.invite
	if p.accepted {
		id := inviteUser
		if p.wrongAcceptedID {
			id = "unexpected_user"
		}
		invite.State = "accepted"
		invite.AcceptedUserID = &id
	}
	return &invite, nil
}

func (p *inviteAcceptanceMock) SendInviteCode(_ context.Context, email string, token *string) error {
	if email != inviteEmail || !p.validMagicToken(token) {
		return errors.New("wrong verification target")
	}
	p.sendCalls++
	return p.sendError
}

func (p *inviteAcceptanceMock) VerifyInviteCode(_ context.Context, email, code string, token *string) (*workos.AuthenticateResponse, error) {
	if email != inviteEmail || !p.validMagicToken(token) {
		return nil, errors.New("wrong verification target")
	}
	if p.verifyError != nil {
		return nil, p.verifyError
	}
	if code != "123456" {
		return nil, &workos.APIError{StatusCode: 400}
	}
	p.accepted = true
	if p.wrongEmail {
		email = "wrong@example.test"
	}
	createdAt := p.invite.CreatedAt
	if p.preexistingUser {
		createdAt = time.Now().Add(-time.Hour).Format(time.RFC3339)
	}
	return &workos.AuthenticateResponse{
		AccessToken: "mock-access-token",
		User:        &workos.User{ID: inviteUser, Email: email, EmailVerified: true, CreatedAt: createdAt},
	}, nil
}

func (p *inviteAcceptanceMock) validMagicToken(token *string) bool {
	if p.accepted {
		return token == nil
	}
	return token != nil && *token == inviteToken
}

func (p *inviteAcceptanceMock) SetInvitePassword(_ context.Context, id, password string) (*workos.User, error) {
	p.passwordCalls.Add(1)
	if p.started != nil {
		close(p.started)
		<-p.release
	}
	if id != inviteUser || password != invitePass {
		return nil, errors.New("wrong password target")
	}
	return &workos.User{ID: id, Email: inviteEmail}, p.passwordError
}

func inviteRequest(path, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Origin", "http://localhost:5173")
	request.Header.Set("Content-Type", "application/json")
	return request
}

func TestInviteRejectsInvalidInputBeforeAccessingDependencies(t *testing.T) {
	server := Server{config: Config{AppURL: "http://localhost:5173"}}
	for _, test := range []struct {
		handler http.HandlerFunc
		body    string
		status  int
	}{
		{server.InviteDetails, `{"token":"bad token"}`, 404},
		{server.VerifyInviteEmail, `{"code":"abc123"}`, 400},
		{server.SetInvitePassword, `{"password":"a","confirmPassword":"b"}`, 400},
		{server.SetInvitePassword, `{"workosUserId":"other","password":"a","confirmPassword":"a"}`, 400},
		{server.SetInvitePassword, invitePasswordRequest, 401},
	} {
		writer := httptest.NewRecorder()
		test.handler(writer, inviteRequest("/auth/invite", test.body))
		if writer.Code != test.status {
			t.Fatalf("got %d, want %d: %s", writer.Code, test.status, writer.Body)
		}
	}
}

func TestInvitationOnboardingPostgres(t *testing.T) {
	db := onboardingDB(t)
	for _, scenario := range []string{"complete", "revoked", "expired", "already accepted", "wrong email", "wrong user", "preexisting user", "provider code expired", "send rejected", "password lookup timed out", "update rejected", "update uncertain", "session expired", "accepted link expired", "local verification failure", "local finalization failure", "concurrent"} {
		t.Run(scenario, func(t *testing.T) {
			if _, err := db.Exec("DELETE FROM invitation_onboarding; DELETE FROM auth_sessions; DELETE FROM users"); err != nil {
				t.Fatal(err)
			}
			localID := uuid.NewString()
			if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_active,workos_invitation_id) VALUES($1,$2,'Admin-entered Name',false,$3)`, localID, inviteEmail, inviteID); err != nil {
				t.Fatal(err)
			}
			provider := &inviteAcceptanceMock{invite: workos.UserInvite{
				ID: inviteID, Email: inviteEmail, State: "pending",
				CreatedAt: time.Now().Add(-time.Minute).Format(time.RFC3339),
				ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339),
			}}
			server := Server{database: db, provider: provider,
				config: Config{AppURL: "http://localhost:5173", CookiePassword: strings.Repeat("x", 32), Secure: true},
				verify: func(_ context.Context, token string) (*claims, error) {
					if token != "mock-access-token" {
						return nil, errors.New("unexpected token")
					}
					return &claims{RegisteredClaims: jwt.RegisteredClaims{Subject: inviteUser}}, nil
				},
			}
			cookies := map[string]*http.Cookie{}
			call := func(handler http.HandlerFunc, body string, want int) *httptest.ResponseRecorder {
				t.Helper()
				writer := httptest.NewRecorder()
				req := inviteRequest("/auth/invite", body)
				for _, cookie := range cookies {
					req.AddCookie(cookie)
				}
				handler(writer, req)
				if writer.Code != want {
					t.Fatalf("got %d, want %d: %s", writer.Code, want, writer.Body)
				}
				for _, cookie := range writer.Result().Cookies() {
					if cookie.Name == "session" || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
						t.Fatalf("unexpected cookie %s", cookie.Name)
					}
					if cookie.MaxAge < 0 {
						delete(cookies, cookie.Name)
					} else {
						cookies[cookie.Name] = cookie
					}
				}
				return writer
			}
			exec := func(query string) {
				t.Helper()
				if _, err := db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "revoked" || scenario == "expired" {
				provider.invite.State = workos.UserInviteState(scenario)
				call(server.InviteDetails, `{"token":"`+inviteToken+`"}`, 410)
				return
			}
			if scenario == "already accepted" {
				provider.accepted = true
				call(server.InviteDetails, `{"token":"`+inviteToken+`"}`, 409)
				return
			}
			call(server.InviteDetails, `{"token":"unknown"}`, 404)
			details := call(server.InviteDetails, `{"token":"`+inviteToken+`"}`, 200)
			if !strings.Contains(details.Body.String(), "Admin-entered Name") || strings.Contains(details.Body.String(), inviteToken) {
				t.Fatalf("unexpected profile: %s", details.Body)
			}
			if scenario == "send rejected" {
				provider.sendError = &workos.APIError{StatusCode: 429}
				call(server.SendInviteCode, `{}`, 429)
				provider.sendError = nil
				call(server.SendInviteCode, `{}`, 429)
				if provider.sendCalls != 1 {
					t.Fatal("provider rejection bypassed the local cooldown")
				}
				if refreshed := call(server.InviteDetails, `{}`, 200); !strings.Contains(refreshed.Body.String(), `"step":"details"`) {
					t.Fatal("failed send appeared as a delivered code")
				}
				call(server.VerifyInviteEmail, `{"code":"123456"}`, 400)
				exec("UPDATE invitation_onboarding SET code_requested_at=now()-interval '61 seconds'")
				provider.sendCalls = 0
			}
			call(server.SendInviteCode, `{}`, 204)
			call(server.SendInviteCode, `{}`, 429)
			if refreshed := call(server.InviteDetails, `{}`, 200); !strings.Contains(refreshed.Body.String(), `"step":"verify"`) {
				t.Fatal("refresh did not restore code entry")
			}
			if provider.sendCalls != 1 {
				t.Fatal("cooldown did not prevent a duplicate email")
			}
			call(server.VerifyInviteEmail, `{"code":"121212"}`, 400)
			if scenario == "wrong email" || scenario == "wrong user" {
				provider.wrongEmail = scenario == "wrong email"
				provider.wrongAcceptedID = scenario == "wrong user"
				call(server.VerifyInviteEmail, `{"code":"123456"}`, 404)
				return
			}
			if scenario == "provider code expired" {
				provider.verifyError = &workos.APIError{StatusCode: 400, Code: "code_expired"}
				call(server.VerifyInviteEmail, `{"code":"123456"}`, 400)
				return
			}
			if scenario == "preexisting user" {
				provider.preexistingUser = true
				call(server.VerifyInviteEmail, `{"code":"123456"}`, 409)
				call(server.SetInvitePassword, invitePasswordRequest, 401)
				return
			}
			if scenario == "local verification failure" {
				exec("ALTER TABLE invitation_onboarding ADD CONSTRAINT test_verify_failure CHECK (workos_id IS NULL)")
				call(server.VerifyInviteEmail, `{"code":"123456"}`, 503)
				exec("ALTER TABLE invitation_onboarding DROP CONSTRAINT test_verify_failure")
				exec("UPDATE invitation_onboarding SET code_requested_at=now()-interval '61 seconds'")
				call(server.SendInviteCode, `{}`, 204)
			}
			call(server.VerifyInviteEmail, `{"code":"123456"}`, 204)
			if cookie := cookies[inviteSessionCookie]; cookie == nil || cookie.MaxAge != 600 {
				t.Fatal("missing ten-minute onboarding session")
			}
			refreshed := call(server.InviteDetails, `{}`, 200)
			if !strings.Contains(refreshed.Body.String(), `"step":"password"`) {
				t.Fatal("refresh did not restore verified step")
			}
			if scenario == "session expired" || scenario == "accepted link expired" {
				exec("UPDATE invitation_onboarding SET expires_at=now()-interval '1 second',code_requested_at=now()-interval '11 minutes',code_sent_at=now()-interval '11 minutes'")
				if scenario == "accepted link expired" {
					provider.invite.ExpiresAt = time.Now().Add(-time.Minute).Format(time.RFC3339)
					delete(cookies, inviteTokenCookie)
					if refreshed := call(server.InviteDetails, `{"token":"`+inviteToken+`"}`, 200); !strings.Contains(refreshed.Body.String(), `"step":"details"`) {
						t.Fatal("accepted invitation did not resume after the link expired")
					}
				}
				call(server.SetInvitePassword, invitePasswordRequest, 401)
				call(server.SendInviteCode, `{}`, 204)
				call(server.VerifyInviteEmail, `{"code":"123456"}`, 204)
			}
			if scenario == "password lookup timed out" {
				ctx, cancel := context.WithCancel(context.Background())
				provider.cancelLookup = cancel
				req := inviteRequest("/auth/invite/password", invitePasswordRequest).WithContext(ctx)
				req.AddCookie(cookies[inviteSessionCookie])
				writer := httptest.NewRecorder()
				server.SetInvitePassword(writer, req)
				cancel()
				if writer.Code != 503 || provider.passwordCalls.Load() != 0 {
					t.Fatalf("canceled lookup returned %d and made %d password updates", writer.Code, provider.passwordCalls.Load())
				}
				provider.cancelLookup = nil
			}
			if scenario == "update rejected" {
				provider.passwordError = &workos.APIError{StatusCode: 422}
				call(server.SetInvitePassword, invitePasswordRequest, 422)
				provider.passwordError = nil
			}
			if scenario == "update uncertain" {
				provider.passwordError = &workos.APIError{StatusCode: 503}
				call(server.SetInvitePassword, invitePasswordRequest, 503)
				call(server.SetInvitePassword, invitePasswordRequest, 409)
				if provider.passwordCalls.Load() != 1 {
					t.Fatal("ambiguous password update was repeated")
				}
				return
			}
			if scenario == "local finalization failure" {
				exec("ALTER TABLE users ADD CONSTRAINT test_link_failure CHECK (workos_id IS NULL)")
				call(server.SetInvitePassword, invitePasswordRequest, 503)
				exec("ALTER TABLE users DROP CONSTRAINT test_link_failure")
				call(server.SetInvitePassword, invitePasswordRequest, 409)
				exec("UPDATE invitation_onboarding SET password_started_at=now()-interval '31 seconds'")
				method := workos.AuthenticateResponseAuthenticationMethodPassword
				account, err := server.loginAccount(context.Background(), &workos.AuthenticateResponse{
					User: &workos.User{ID: inviteUser, Email: inviteEmail, EmailVerified: true}, AuthenticationMethod: &method,
				})
				if err != nil {
					t.Fatal(err)
				}
				tx, err := db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if err := linkLoginAccount(context.Background(), tx, account, &workos.User{ID: inviteUser, Email: inviteEmail}); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			} else if scenario == "concurrent" {
				provider.started, provider.release = make(chan struct{}), make(chan struct{})
				req := inviteRequest("/auth/invite/password", invitePasswordRequest)
				req.AddCookie(cookies[inviteSessionCookie])
				first := httptest.NewRecorder()
				done := make(chan struct{})
				go func() { defer close(done); server.SetInvitePassword(first, req) }()
				select {
				case <-provider.started:
				case <-time.After(5 * time.Second):
					t.Fatal("first request never reached WorkOS")
				}
				func() {
					defer func() { close(provider.release); <-done }()
					call(server.SetInvitePassword, invitePasswordRequest, 409)
				}()
				if first.Code != 204 || provider.passwordCalls.Load() != 1 {
					t.Fatalf("concurrent completion: %d, %d PUTs", first.Code, provider.passwordCalls.Load())
				}
			} else {
				call(server.SetInvitePassword, invitePasswordRequest, 204)
			}
			call(server.SetInvitePassword, invitePasswordRequest, 401)
			var id, displayName string
			var active bool
			var sessions, onboarding int
			if err := db.QueryRow(`SELECT workos_id,display_name,is_active,
				(SELECT count(*) FROM auth_sessions),(SELECT count(*) FROM invitation_onboarding) FROM users WHERE id=$1`, localID).
				Scan(&id, &displayName, &active, &sessions, &onboarding); err != nil {
				t.Fatal(err)
			}
			if id != inviteUser || displayName != "Admin-entered Name" || active || sessions != 0 || onboarding != 0 {
				t.Fatalf("unexpected linked account: %s %s active=%t sessions=%d onboarding=%d", id, displayName, active, sessions, onboarding)
			}
		})
	}
}

func onboardingDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("RBAC_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("RBAC_TEST_DATABASE_URL not configured")
	}
	base, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "invite_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	t.Cleanup(func() {
		if _, err := base.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE"); err != nil {
			t.Error(err)
		}
		base.Close()
	})
	if _, err := base.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("postgres", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	files, err := filepath.Glob("../migrations/*.up.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("migrations: %v", err)
	}
	for _, file := range files {
		migration, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(migration)); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
	return db
}
