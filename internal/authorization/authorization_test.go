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
	resourceID := "00000000-0000-4000-8000-000000000001"
	mock.ExpectQuery("SELECT EXISTS").WithArgs("user-id", "schedules.view", resourceID).
		WillReturnRows(sqlmock.NewRows([]string{"allowed"}).AddRow(true))
	allowed, err := service.HasResourcePermission(context.Background(), "user-id", "schedules.view", resourceID)
	if err != nil || !allowed {
		t.Fatalf("allowed=%v err=%v", allowed, err)
	}
	mock.ExpectQuery("SELECT EXISTS").WithArgs("user-id", "users.manage", nil).
		WillReturnError(errors.New("unavailable"))
	if _, err := service.HasGlobalPermission(context.Background(), "user-id", "users.manage"); err == nil {
		t.Fatal("database error must not grant permission")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMiddlewareRejectsMissingIdentity(t *testing.T) {
	service := &Service{}
	handler := service.HasPermissionMiddleware("users.view", func(context.Context) (string, bool) { return "", false }, nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler reached without authentication")
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/admin/users", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", response.Code)
	}
}
