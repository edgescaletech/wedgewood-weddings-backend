package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWeekBounds(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		expectedStart string
		expectedEnd   string
		expectErr     bool
	}{
		{
			name:          "explicit Monday input",
			input:         "2026-11-16",
			expectedStart: "2026-11-16",
			expectedEnd:   "2026-11-22",
			expectErr:     false,
		},
		{
			name:          "midweek Friday snaps to Monday",
			input:         "2026-11-20",
			expectedStart: "2026-11-16",
			expectedEnd:   "2026-11-22",
			expectErr:     false,
		},
		{
			name:          "Sunday snaps to preceding Monday",
			input:         "2026-11-22",
			expectedStart: "2026-11-16",
			expectedEnd:   "2026-11-22",
			expectErr:     false,
		},
		{
			name:      "invalid date string returns error",
			input:     "not-a-date",
			expectErr: true,
		},
		{
			name:          "empty input defaults to current week Monday",
			input:         "",
			expectErr:     false,
			expectedStart: "", // verified via Weekday()
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			start, end, err := weekBounds(tc.input)
			if tc.expectErr {
				if err == nil {
					t.Fatalf("expected error for input %q, got nil", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for input %q: %v", tc.input, err)
			}

			if tc.expectedStart != "" && start.Format("2006-01-02") != tc.expectedStart {
				t.Errorf("start date: got %s, want %s", start.Format("2006-01-02"), tc.expectedStart)
			}
			if tc.expectedEnd != "" && end.Format("2006-01-02") != tc.expectedEnd {
				t.Errorf("end date: got %s, want %s", end.Format("2006-01-02"), tc.expectedEnd)
			}
			if start.Weekday() != time.Monday {
				t.Errorf("start must always be Monday, got %s", start.Weekday())
			}
			if end.Weekday() != time.Sunday {
				t.Errorf("end must always be Sunday, got %s", end.Weekday())
			}
		})
	}
}

func TestHealthzHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	healthzHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("healthz status: got %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("got status %q, want %q", body["status"], "ok")
	}
}

func TestEventsHandler_Validation(t *testing.T) {
	handler := eventsHandler(nil, nil, nil)

	t.Run("invalid week parameter returns 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/events?week=invalid", nil)
		rec := httptest.NewRecorder()

		handler(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("status code: got %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})
}

func TestEventByIDHandler_Validation(t *testing.T) {
	handler := eventByIDHandler(nil, nil)

	t.Run("empty event id returns 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/events/", nil)
		rec := httptest.NewRecorder()

		handler(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("status code: got %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})
}

func TestSyncEventsHandler_Validation(t *testing.T) {
	handler := syncEventsHandler(nil)

	t.Run("invalid week parameter returns 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/sync/events?week=bad", nil)
		rec := httptest.NewRecorder()

		handler(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("status code: got %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("valid week parameter returns 202 accepted", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/sync/events?week=2026-11-16", nil)
		rec := httptest.NewRecorder()

		handler(rec, req)

		if rec.Code != http.StatusAccepted {
			t.Errorf("status code: got %d, want %d", rec.Code, http.StatusAccepted)
		}

		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse json response: %v", err)
		}
		if resp["status"] != "accepted" {
			t.Errorf("got status %v, want accepted", resp["status"])
		}
		if resp["weekStart"] != "2026-11-16" {
			t.Errorf("got weekStart %v, want 2026-11-16", resp["weekStart"])
		}
	})
}

func TestParseFlexibleDate(t *testing.T) {
	testDates := []struct {
		input    string
		wantYear int
		wantMonth time.Month
		wantDay  int
	}{
		{"Sun Jan 1, 2023", 2023, time.January, 1},
		{"Mon Jan 30, 2023", 2023, time.January, 30},
		{"Mon Feb 27, 2023", 2023, time.February, 27},
		{"Mon Apr 3, 2023", 2023, time.April, 3},
		{"Thu Jan 1, 2026", 2026, time.January, 1},
		{"Mon Nov 30, 2026", 2026, time.November, 30},
		{"2026-11-30", 2026, time.November, 30},
		{"11/30/2026", 2026, time.November, 30},
		{"1/1/2026", 2026, time.January, 1},
	}

	for _, tc := range testDates {
		parsed, err := parseFlexibleDate(tc.input)
		if err != nil {
			t.Errorf("parseFlexibleDate(%q) failed: %v", tc.input, err)
			continue
		}
		if parsed.Year() != tc.wantYear || parsed.Month() != tc.wantMonth || parsed.Day() != tc.wantDay {
			t.Errorf("parseFlexibleDate(%q) = %v; want %04d-%02d-%02d", tc.input, parsed, tc.wantYear, tc.wantMonth, tc.wantDay)
		}
	}
}
