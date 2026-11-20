package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/golang-jwt/jwt/v5"
	"github.com/lib/pq"
	workos "github.com/workos/workos-go/v7"
)

type inviteLoginProvider struct {
	fakeProvider
	invitation *workos.UserInvite
}

func (provider inviteLoginProvider) GetInvitation(context.Context, string) (*workos.UserInvite, error) {
	return provider.invitation, nil
}

func TestInvitationLoginRequiresExactAcceptedIdentity(t *testing.T) {
	for _, scenario := range []struct {
		label, acceptedID, state string
		hasPending               bool
		status                   int
	}{
		{"unknown identity", "", "", false, 403},
		{"wrong identity", "user_other", "accepted", true, 403},
		{"expired", "user_verified", "expired", true, 403},
		{"accepted", "user_verified", "accepted", true, 204},
	} {
		t.Run(scenario.label, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			const email = "invite@example.com"
			const subject = "user_verified"
			server := &Server{database: database, config: Config{CookiePassword: strings.Repeat("a", 32)},
				provider: &inviteLoginProvider{invitation: &workos.UserInvite{ID: "inv_1", Email: email, State: workos.UserInviteState(scenario.state), AcceptedUserID: &scenario.acceptedID}},
				verify: func(context.Context, string) (*claims, error) {
					return &claims{RegisteredClaims: jwt.RegisteredClaims{Subject: subject}}, nil
				},
			}
			mock.ExpectQuery("SELECT id FROM users WHERE workos_id").WithArgs(subject).WillReturnRows(sqlmock.NewRows([]string{"id"}))
			pending := sqlmock.NewRows([]string{"id", "workos_invitation_id"})
			if scenario.hasPending {
				pending.AddRow("local", "inv_1")
			}
			mock.ExpectQuery("SELECT u.id,u.workos_invitation_id").WithArgs(email, subject).WillReturnRows(pending)
			if scenario.status == 204 {
				mock.ExpectBegin()
				mock.ExpectExec("UPDATE users SET workos_id").WithArgs(subject, email, "local", "inv_1").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("INSERT INTO audit_logs").WithArgs("local", "inv_1").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("DELETE FROM invitation_onboarding").WithArgs("inv_1", "local").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("INSERT INTO auth_sessions").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			}
			method := workos.AuthenticateResponseAuthenticationMethodPassword
			response := httptest.NewRecorder()
			server.createSession(response, httptest.NewRequest(http.MethodPost, "/auth/password", nil), &workos.AuthenticateResponse{
				AuthenticationMethod: &method, User: &workos.User{ID: subject, Email: email, EmailVerified: true}, AccessToken: "access", RefreshToken: "refresh",
			}, true)
			if response.Code != scenario.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSignInWithConflictingEmailReturnsConflict(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := Server{database: db, config: Config{CookiePassword: strings.Repeat("a", 32)}, verify: func(context.Context, string) (*claims, error) {
		return &claims{RegisteredClaims: jwt.RegisteredClaims{Subject: "linked-user"}}, nil
	}}
	mock.ExpectQuery("SELECT id FROM users WHERE workos_id").WithArgs("linked-user").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("local"))
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE users SET workos_id").WillReturnError(&pq.Error{Code: "23505", Constraint: "users_email_owner"})
	mock.ExpectRollback()
	writer := httptest.NewRecorder()
	server.createSession(writer, httptest.NewRequest(http.MethodPost, "/auth/password", nil), &workos.AuthenticateResponse{
		User: &workos.User{ID: "linked-user", Email: "owned@example.com", EmailVerified: true}, AccessToken: "access", RefreshToken: "refresh",
	}, true)
	if writer.Code != http.StatusConflict || writer.Header().Get("Set-Cookie") != "" {
		t.Fatalf("unexpected sign-in response: %d", writer.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
