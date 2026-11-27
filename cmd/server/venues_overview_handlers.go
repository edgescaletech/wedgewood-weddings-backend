package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"wedding-project/internal/models"
	"wedding-project/internal/repositories"
)

// venuesOverviewHandler returns all venues with their targets for a specified week and month.
func venuesOverviewHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "GET required")
			return
		}

		weekStr := r.URL.Query().Get("week")
		region := r.URL.Query().Get("region")
		search := r.URL.Query().Get("search")

		anchor := time.Now()
		if weekStr != "" {
			if parsed, err := time.Parse("2006-01-02", weekStr); err == nil {
				anchor = parsed
			}
		}

		resp, err := repositories.GetVenuesOverview(r.Context(), db, anchor, region, search)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

// regionsListHandler returns all distinct reporting regions with venue counts.
func regionsListHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "GET required")
			return
		}

		summaries, err := repositories.GetRegionSummaries(r.Context(), db)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"regions": summaries,
			"count":   len(summaries),
		})
	}
}

// updateRegionHandler assigns venues to a reporting region.
func updateRegionHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}

		var req models.UpdateRegionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}

		req.RegionName = strings.TrimSpace(req.RegionName)
		if req.RegionName == "" {
			writeError(w, http.StatusBadRequest, "regionName is required")
			return
		}
		if strings.EqualFold(req.RegionName, "ungrouped") {
			writeError(w, http.StatusBadRequest, "cannot name a region 'Ungrouped' (reserved for unassigned venues)")
			return
		}

		if len(req.VenueIDs) == 0 {
			writeError(w, http.StatusBadRequest, "at least one venue ID must be selected")
			return
		}

		rowsUpdated, err := repositories.BulkUpdateVenueRegion(r.Context(), db, req.RegionName, req.VenueIDs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"success":    true,
			"regionName": req.RegionName,
			"updated":    rowsUpdated,
		})
	}
}

// regionsHandler combines list (GET) and update (POST) for /api/admin/regions.
func regionsHandler(db *sql.DB) http.HandlerFunc {
	list := regionsListHandler(db)
	update := updateRegionHandler(db)
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			list(w, r)
		case http.MethodPost:
			update(w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
	}
}
