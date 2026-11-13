package authorization

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestHasPermissionQueriesDatabase(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := &Service{DB: db}
	venue := "salesforce-venue"
	mock.ExpectQuery("WITH membership").WithArgs("user-id", "schedules.view", venue).
		WillReturnRows(sqlmock.NewRows([]string{"allowed"}).AddRow(true))
	allowed, err := service.HasPermission(context.Background(), "user-id", "schedules.view", &venue)
	if err != nil || !allowed {
		t.Fatalf("allowed=%v err=%v", allowed, err)
	}
	mock.ExpectQuery("WITH membership").WithArgs("user-id", "users.manage", nil).
		WillReturnError(errors.New("unavailable"))
	if _, err := service.HasPermission(context.Background(), "user-id", "users.manage", &venue); err == nil {
		t.Fatal("database error must not grant permission")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMiddlewareRejectsMissingIdentity(t *testing.T) {
	service := &Service{}
	handler := service.HasPermissionMiddleware("users.view", nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler reached without authentication")
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/admin/users", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", response.Code)
	}
}
