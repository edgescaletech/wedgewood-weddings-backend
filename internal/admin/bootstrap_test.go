package admin

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

type verifierFunc func(context.Context, string, string, string) error

func (verify verifierFunc) Verify(ctx context.Context, user, organization, role string) error {
	return verify(ctx, user, organization, role)
}

func TestBootstrapRejectsUnverifiedUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	verifier := verifierFunc(func(context.Context, string, string, string) error { return errors.New("not verified") })
	if err := Bootstrap(context.Background(), db, verifier, "user", "org", "role"); err == nil {
		t.Fatal("unverified identity accepted")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapCommitsAssignmentAndAudit(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	verifier := verifierFunc(func(_ context.Context, user, org, role string) error {
		if user != "workos-user" || org != "org" || role != "bootstrap" {
			return errors.New("wrong identity")
		}
		return nil
	})
	mock.ExpectBegin()
	mock.ExpectExec("pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT id FROM users").WithArgs("workos-user").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("local-user"))
	mock.ExpectExec("INSERT INTO admin_bootstrap").WithArgs("local-user").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE users").WithArgs("local-user").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO user_roles").WithArgs(sqlmock.AnyArg(), "local-user").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO audit_logs").WithArgs(sqlmock.AnyArg(), "local-user").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := Bootstrap(context.Background(), db, verifier, "workos-user", "org", "bootstrap"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
