package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVenuesOverviewHandler_MethodValidation(t *testing.T) {
	handler := venuesOverviewHandler(nil)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/venues-overview", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("venuesOverviewHandler non-GET: got status %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestRegionsListHandler_MethodValidation(t *testing.T) {
	handler := regionsListHandler(nil)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/regions", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("regionsListHandler non-GET: got status %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestUpdateRegionHandler_Validation(t *testing.T) {
	handler := updateRegionHandler(nil)

	t.Run("non-POST method returns 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/regions", nil)
		rec := httptest.NewRecorder()

		handler(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("got status %d, want %d", rec.Code, http.StatusMethodNotAllowed)
		}
	})

	t.Run("invalid json body returns 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/admin/regions", bytes.NewBufferString("{invalid-json"))
		rec := httptest.NewRecorder()

		handler(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("got status %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("empty region name returns 400", func(t *testing.T) {
		payload, _ := json.Marshal(map[string]any{
			"regionName": "   ",
			"venueIds":   []string{"v1", "v2"},
		})
		req := httptest.NewRequest(http.MethodPost, "/api/admin/regions", bytes.NewBuffer(payload))
		rec := httptest.NewRecorder()

		handler(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("got status %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("reserved region name Ungrouped returns 400", func(t *testing.T) {
		payload, _ := json.Marshal(map[string]any{
			"regionName": "Ungrouped",
			"venueIds":   []string{"v1", "v2"},
		})
		req := httptest.NewRequest(http.MethodPost, "/api/admin/regions", bytes.NewBuffer(payload))
		rec := httptest.NewRecorder()

		handler(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("got status %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})
}
