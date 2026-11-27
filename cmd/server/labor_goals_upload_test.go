package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestLaborGoalsUploadHandler_MethodValidation(t *testing.T) {
	handler := laborGoalsUploadHandler(nil)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/labor-goals/upload", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("laborGoalsUploadHandler non-POST: got status %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestLaborGoalsUploadHandler_EmptyBody(t *testing.T) {
	handler := laborGoalsUploadHandler(nil)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/labor-goals/upload", bytes.NewBufferString(""))
	req.Header.Set("Content-Type", "text/csv")
	rec := httptest.NewRecorder()

	handler(rec, req)

	// Since DB is nil, it either errors early on empty or 500 on DB connection
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 400 or 500 for empty csv body, got %d", rec.Code)
	}
}

func TestProcessLaborGoalsCSV_MissingHeaders(t *testing.T) {
	// Missing mandatory columns like FOH / BOH
	csvData := `Venue,Month
Clayton House,2026-01
`
	res, err := processLaborGoalsCSV(t.Context(), nil, strings.NewReader(csvData))
	if err == nil {
		t.Fatalf("expected error for missing FOH/BOH columns, got nil, result=%+v", res)
	}
	if !strings.Contains(err.Error(), "CSV must have columns") {
		t.Errorf("expected 'CSV must have columns' error message, got: %v", err)
	}
}

func TestProcessLaborGoalsCSV_TargetParsingAndReupload(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	// 1. Both present (FOH: 2.23, BOH: 3.51)
	// 2. FOH blank, BOH present (FOH: NULL, BOH: 3.69)
	// 3. BOH blank, FOH present (FOH: 2.45, BOH: NULL)
	// 4. Both blank (FOH: NULL, BOH: NULL)
	// 5. Invalid text in FOH ("invalid_foh", should skip)
	// 6. Re-upload clearing: re-uploading a month with blank target replacing prior values
	csvData := `Venue,Venue #,Month,Region,FOH guests per labor hour,BOH guests per labor hour
Boulder Ridge,101,2027-01,Bay Area,2.23,3.51
Boulder Ridge,101,2027-02,Bay Area,,3.69
Boulder Ridge,101,2027-03,Bay Area,2.45,
Boulder Ridge,101,2027-04,Bay Area,,
Boulder Ridge,101,2027-05,Bay Area,invalid_foh,4.00
Boulder Ridge,101,2027-01,Bay Area,,
`

	// Row 1: Both present (2.23, 3.51)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM venues WHERE TRIM(venue_number) = TRIM($1)")).
		WithArgs("101").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("venue-1"))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE venues SET venue_number = COALESCE(NULLIF($2, ''), venue_number)")).
		WithArgs("venue-1", "101", "Bay Area").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO labor_efficiency_targets")).
		WithArgs("venue-1", "101", 2027, "2027-01", 2.23, 3.51).
		WillReturnResult(sqlmock.NewResult(1, 1))

	// Row 2: FOH blank, BOH present (nil, 3.69)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM venues WHERE TRIM(venue_number) = TRIM($1)")).
		WithArgs("101").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("venue-1"))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE venues SET venue_number = COALESCE(NULLIF($2, ''), venue_number)")).
		WithArgs("venue-1", "101", "Bay Area").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO labor_efficiency_targets")).
		WithArgs("venue-1", "101", 2027, "2027-02", nil, 3.69).
		WillReturnResult(sqlmock.NewResult(1, 1))

	// Row 3: BOH blank, FOH present (2.45, nil)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM venues WHERE TRIM(venue_number) = TRIM($1)")).
		WithArgs("101").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("venue-1"))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE venues SET venue_number = COALESCE(NULLIF($2, ''), venue_number)")).
		WithArgs("venue-1", "101", "Bay Area").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO labor_efficiency_targets")).
		WithArgs("venue-1", "101", 2027, "2027-03", 2.45, nil).
		WillReturnResult(sqlmock.NewResult(1, 1))

	// Row 4: Both blank (nil, nil)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM venues WHERE TRIM(venue_number) = TRIM($1)")).
		WithArgs("101").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("venue-1"))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE venues SET venue_number = COALESCE(NULLIF($2, ''), venue_number)")).
		WithArgs("venue-1", "101", "Bay Area").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO labor_efficiency_targets")).
		WithArgs("venue-1", "101", 2027, "2027-04", nil, nil).
		WillReturnResult(sqlmock.NewResult(1, 1))

	// Row 5: invalid FOH ("invalid_foh") -> should be skipped, no DB calls

	// Row 6: Re-upload 2027-01 clearing previously set 2.23/3.51 with blank (nil, nil)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM venues WHERE TRIM(venue_number) = TRIM($1)")).
		WithArgs("101").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("venue-1"))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE venues SET venue_number = COALESCE(NULLIF($2, ''), venue_number)")).
		WithArgs("venue-1", "101", "Bay Area").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO labor_efficiency_targets")).
		WithArgs("venue-1", "101", 2027, "2027-01", nil, nil).
		WillReturnResult(sqlmock.NewResult(1, 1))

	res, err := processLaborGoalsCSV(t.Context(), db, strings.NewReader(csvData))
	if err != nil {
		t.Fatalf("processLaborGoalsCSV failed: %v", err)
	}

	if res.Imported != 5 {
		t.Errorf("expected 5 imported rows, got %d", res.Imported)
	}
	if res.Skipped != 1 {
		t.Errorf("expected 1 skipped row (invalid text), got %d", res.Skipped)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "invalid_foh") {
		t.Errorf("expected error for invalid_foh, got: %v", res.Errors)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet mock expectations: %v", err)
	}
}
