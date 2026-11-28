package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"wedding-project/internal/models"
	"wedding-project/internal/repositories"
	"wedding-project/internal/services"
)

// csvUploadResult is the JSON response body for a CSV import request.
type csvUploadResult struct {
	Imported int      `json:"imported"`
	Updated  int      `json:"updated"`
	Skipped  int      `json:"skipped"`
	Errors   []string `json:"errors"`
}

// ---------------------------------------------------------------------------
// Labor goals query handlers
// ---------------------------------------------------------------------------

func laborGoalsSummaryHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		venueID := r.URL.Query().Get("venueId")
		weekStr := r.URL.Query().Get("week")
		anchor := time.Now()
		if weekStr != "" {
			if parsed, err := time.Parse("2006-01-02", weekStr); err == nil {
				anchor = parsed
			}
		}

		summary, err := services.BuildLaborGoalsSummary(r.Context(), db, venueID, anchor)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, summary)
	}
}

func laborGoalsTargetsHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		venueID := r.URL.Query().Get("venueId")
		region := r.URL.Query().Get("region")
		yearStr := strings.TrimSpace(r.URL.Query().Get("year"))
		if yearStr == "" {
			writeError(w, http.StatusBadRequest, "year is required")
			return
		}
		year, err := strconv.Atoi(yearStr)
		if err != nil || year < 1 || year > 9999 {
			writeError(w, http.StatusBadRequest, "invalid year: must be an integer between 1 and 9999")
			return
		}

		targets, err := repositories.GetLaborTargets(r.Context(), db, venueID, region, year)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		allowed := eventVenueAccess(r)
		visible := make([]models.LaborEfficiencyTarget, 0, len(targets))
		for _, target := range targets {
			if allowed[target.VenueID] {
				visible = append(visible, target)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"venueId": venueID,
			"region":  region,
			"year":    year,
			"targets": visible,
		})
	}
}

// venuesListHandler returns active venues authorized by the venueAccess middleware.
func venuesListHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		venues, err := repositories.GetAllActiveVenues(r.Context(), db)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		allowed := eventVenueAccess(r)
		visible := make([]models.Venue, 0, len(venues))
		for _, venue := range venues {
			if allowed[venue.ID] {
				visible = append(visible, venue)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"venues": visible,
			"count":  len(visible),
		})
	}
}

func accountingPeriodsHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		yearStr := r.URL.Query().Get("year")
		year := 2026
		if yearStr != "" {
			if parsed, err := strconv.Atoi(yearStr); err == nil && parsed > 0 {
				year = parsed
			}
		}

		periods, err := repositories.GetAccountingPeriodsByYear(r.Context(), db, year)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if periods == nil {
			periods = []models.AccountingPeriod{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"year":    year,
			"periods": periods,
		})
	}
}

// ---------------------------------------------------------------------------
// Labor goals CSV upload handler
// ---------------------------------------------------------------------------

// laborGoalsUploadHandler handles POST /api/admin/labor-goals/upload.
//
// Accepted CSV columns (case-insensitive header matching):
//
//	Venue, Month, FOH guests per labor hour, BOH guests per labor hour
//
// Optionally a leading "Venue #" / "Venue Number" column is tolerated and
// stored as venue_number for future reference.
func laborGoalsUploadHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}

		log.Printf("[CSV Import: Labor Targets] Received upload request (Content-Type: %s)", r.Header.Get("Content-Type"))

		var reader io.Reader

		ct := r.Header.Get("Content-Type")
		switch {
		case strings.HasPrefix(ct, "multipart/form-data"):
			if err := r.ParseMultipartForm(32 << 20); err != nil {
				log.Printf("[CSV Import: Labor Targets ERROR] Multipart parse error: %v", err)
				writeError(w, http.StatusBadRequest, "multipart parse error: "+err.Error())
				return
			}
			f, header, err := r.FormFile("file")
			if err != nil {
				log.Printf("[CSV Import: Labor Targets ERROR] Missing 'file' field: %v", err)
				writeError(w, http.StatusBadRequest, "missing 'file' field: "+err.Error())
				return
			}
			defer f.Close()
			log.Printf("[CSV Import: Labor Targets] Uploading file %q (%d bytes)", header.Filename, header.Size)
			reader = f

		case strings.HasPrefix(ct, "application/json"):
			var body struct {
				CSV string `json:"csv"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				log.Printf("[CSV Import: Labor Targets ERROR] JSON parse error: %v", err)
				writeError(w, http.StatusBadRequest, "JSON parse error: "+err.Error())
				return
			}
			log.Printf("[CSV Import: Labor Targets] Parsing pasted CSV (%d bytes)", len(body.CSV))
			reader = strings.NewReader(body.CSV)

		default:
			log.Printf("[CSV Import: Labor Targets] Processing raw CSV body")
			reader = r.Body
		}

		result, err := processLaborGoalsCSV(r.Context(), db, reader)
		if err != nil {
			log.Printf("[CSV Import: Labor Targets ERROR] CSV processing failed: %v", err)
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		log.Printf("[CSV Import: Labor Targets SUCCESS] Imported: %d rows, Skipped: %d rows, Warnings/Errors: %d",
			result.Imported, result.Skipped, len(result.Errors))
		for _, warn := range result.Errors {
			log.Printf("  └─ Warning: %s", warn)
		}

		writeJSON(w, http.StatusOK, result)
	}
}

// processLaborGoalsCSV parses a CSV reader, looks up venues by name/number, and upserts rows.
func processLaborGoalsCSV(ctx context.Context, db *sql.DB, r io.Reader) (*csvUploadResult, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	cr.FieldsPerRecord = -1 // tolerate variable column counts

	var headers []string
	colIdx := map[string]int{
		"venue": -1, "month": -1,
		"foh": -1, "boh": -1, "venuenum": -1, "region": -1,
	}

	lineNum := 0
	// Scan up to 20 initial rows to locate the real header row, skipping title/metadata banners
	for lineNum < 20 {
		lineNum++
		row, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}

		cleanRow := make([]string, len(row))
		curCol := map[string]int{
			"venue": -1, "month": -1,
			"foh": -1, "boh": -1, "venuenum": -1, "region": -1,
		}
		for i, h := range row {
			clean := strings.ToLower(strings.TrimSpace(h))
			cleanRow[i] = clean
			switch {
			case clean == "venue" || clean == "venue name" || clean == "property" || clean == "property name":
				curCol["venue"] = i
			case clean == "month" || clean == "month (yyyy-mm)" || clean == "period" || clean == "period key":
				curCol["month"] = i
			case strings.HasPrefix(clean, "foh"):
				curCol["foh"] = i
			case strings.HasPrefix(clean, "boh"):
				curCol["boh"] = i
			case clean == "venue #" || clean == "venue number" || clean == "venue#" || clean == "unit" || clean == "unit #":
				curCol["venuenum"] = i
			case clean == "region" || clean == "market" || clean == "area":
				curCol["region"] = i
			}
		}

		if (curCol["venue"] != -1 || curCol["venuenum"] != -1) && curCol["month"] != -1 && curCol["foh"] != -1 && curCol["boh"] != -1 {
			headers = cleanRow
			colIdx = curCol
			break
		}
	}

	if (colIdx["venue"] == -1 && colIdx["venuenum"] == -1) || colIdx["month"] == -1 || colIdx["foh"] == -1 || colIdx["boh"] == -1 {
		return nil, fmt.Errorf("CSV must have columns: Venue (or Venue #), Month, FOH guests per labor hour, BOH guests per labor hour (got: %v)", strings.Join(headers, ", "))
	}

	result := &csvUploadResult{Errors: []string{}}

	for {
		lineNum++
		record, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("line %d: CSV read error: %v", lineNum, err))
			result.Skipped++
			continue
		}

		get := func(key string) string {
			idx := colIdx[key]
			if idx < 0 || idx >= len(record) {
				return ""
			}
			return strings.TrimSpace(record[idx])
		}

		venueName := get("venue")
		periodKey := get("month")
		fohStr := get("foh")
		bohStr := get("boh")
		venueNum := get("venuenum")
		region := get("region")

		if venueName == "" || periodKey == "" {
			result.Skipped++
			continue
		}

		// An absent cell is malformed input, not an intentional blank target.
		// Validate positions before any venue metadata or target writes.
		var missing []string
		for _, key := range []string{"foh", "boh"} {
			if colIdx[key] >= len(record) {
				missing = append(missing, strings.ToUpper(key))
			}
		}
		if len(missing) > 0 {
			result.Errors = append(result.Errors, fmt.Sprintf("line %d: missing %s target cells; retain empty CSV cells to clear targets intentionally", lineNum, strings.Join(missing, " and ")))
			result.Skipped++
			continue
		}

		// Parse period key to extract year (e.g. "2026-11" → 2026)
		parts := strings.SplitN(periodKey, "-", 2)
		if len(parts) != 2 {
			result.Errors = append(result.Errors, fmt.Sprintf("line %d: invalid Month format %q (want YYYY-MM)", lineNum, periodKey))
			result.Skipped++
			continue
		}
		year, err := strconv.Atoi(parts[0])
		if err != nil || year < 2000 || year > 2100 {
			result.Errors = append(result.Errors, fmt.Sprintf("line %d: bad year in period key %q", lineNum, periodKey))
			result.Skipped++
			continue
		}
		var fohPtr *float64
		if fohStr != "" {
			v, err := strconv.ParseFloat(fohStr, 64)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("line %d: FOH %q is not a number", lineNum, fohStr))
				result.Skipped++
				continue
			}
			fohPtr = &v
		}

		var bohPtr *float64
		if bohStr != "" {
			v, err := strconv.ParseFloat(bohStr, 64)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("line %d: BOH %q is not a number", lineNum, bohStr))
				result.Skipped++
				continue
			}
			bohPtr = &v
		}

		// 1. Try matching by venue_number first (most reliable)
		var venueID string
		if venueNum != "" {
			venueID, err = repositories.LookupVenueIDByNumber(ctx, db, venueNum)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("line %d: error looking up venue #%s: %v", lineNum, venueNum, err))
			}
		}

		// 2. Fallback to name lookup if not found by number or number was empty
		if venueID == "" && venueName != "" {
			venueID, err = repositories.LookupVenueIDByName(ctx, db, venueName)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("line %d: venue lookup error for %q: %v", lineNum, venueName, err))
				result.Skipped++
				continue
			}
		}

		if venueID == "" {
			result.Errors = append(result.Errors, fmt.Sprintf("line %d: venue %q (#%s) not found in database — skipped", lineNum, venueName, venueNum))
			result.Skipped++
			continue
		}

		// 3. Update venues table with venue_number and region if available
		cleanRegion := strings.TrimSpace(region)
		if strings.EqualFold(cleanRegion, "ungrouped") {
			// Explicitly setting region to "Ungrouped" clears the region to NULL (unassigned)
			_ = repositories.UpdateVenueMetadata(ctx, db, venueID, venueNum, "", true)
		} else if venueNum != "" || cleanRegion != "" {
			// Normal update: if cleanRegion is non-empty, sets region; if empty, preserves existing
			_ = repositories.UpdateVenueMetadata(ctx, db, venueID, venueNum, cleanRegion, false)
		}

		target := &models.LaborEfficiencyTarget{
			VenueID:     venueID,
			VenueNumber: venueNum,
			Year:        year,
			PeriodKey:   periodKey,
			FOHTarget:   fohPtr,
			BOHTarget:   bohPtr,
		}

		if err := repositories.UpsertLaborTarget(ctx, db, target); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("line %d: DB error: %v", lineNum, err))
			result.Skipped++
			continue
		}

		result.Imported++
	}

	return result, nil
}

// ---------------------------------------------------------------------------
// Accounting Periods CSV upload handler
// ---------------------------------------------------------------------------

// accountingPeriodsUploadHandler handles POST /api/admin/accounting-periods/upload.
func accountingPeriodsUploadHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}

		log.Printf("[CSV Import: Accounting Periods] Received upload request (Content-Type: %s)", r.Header.Get("Content-Type"))

		var reader io.Reader

		ct := r.Header.Get("Content-Type")
		switch {
		case strings.HasPrefix(ct, "multipart/form-data"):
			if err := r.ParseMultipartForm(32 << 20); err != nil {
				log.Printf("[CSV Import: Accounting Periods ERROR] Multipart parse error: %v", err)
				writeError(w, http.StatusBadRequest, "multipart parse error: "+err.Error())
				return
			}
			f, header, err := r.FormFile("file")
			if err != nil {
				log.Printf("[CSV Import: Accounting Periods ERROR] Missing 'file' field: %v", err)
				writeError(w, http.StatusBadRequest, "missing 'file' field: "+err.Error())
				return
			}
			defer f.Close()
			log.Printf("[CSV Import: Accounting Periods] Uploading file %q (%d bytes)", header.Filename, header.Size)
			reader = f

		case strings.HasPrefix(ct, "application/json"):
			var body struct {
				CSV string `json:"csv"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				log.Printf("[CSV Import: Accounting Periods ERROR] JSON parse error: %v", err)
				writeError(w, http.StatusBadRequest, "JSON parse error: "+err.Error())
				return
			}
			log.Printf("[CSV Import: Accounting Periods] Parsing pasted CSV (%d bytes)", len(body.CSV))
			reader = strings.NewReader(body.CSV)

		default:
			log.Printf("[CSV Import: Accounting Periods] Processing raw CSV body")
			reader = r.Body
		}

		result, err := processAccountingPeriodsCSV(r.Context(), db, reader)
		if err != nil {
			log.Printf("[CSV Import: Accounting Periods ERROR] CSV processing failed: %v", err)
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		log.Printf("[CSV Import: Accounting Periods SUCCESS] Imported: %d periods, Skipped: %d periods, Warnings/Errors: %d",
			result.Imported, result.Skipped, len(result.Errors))
		for _, warn := range result.Errors {
			log.Printf("  └─ Warning: %s", warn)
		}

		writeJSON(w, http.StatusOK, result)
	}
}

func parseFlexibleDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	formats := []string{
		"Mon Jan 2, 2006",
		"Mon Jan 02, 2006",
		"Mon Jan _2, 2006",
		"Mon, Jan 2, 2006",
		"Mon, Jan 02, 2006",
		"Monday, Jan 2, 2006",
		"Monday, Jan 02, 2006",
		"Monday, January 2, 2006",
		"Monday, January 02, 2006",
		"Mon Jan 2 2006",
		"Mon Jan 02 2006",
		"2006-01-02",
		"2006-1-2",
		"01/02/2006",
		"1/2/2006",
		"1/02/2006",
		"01/2/2006",
		"2006/01/02",
		"2006/1/2",
		"02-Jan-2006",
		"2-Jan-2006",
		"Jan 2, 2006",
		"Jan 02, 2006",
		"January 2, 2006",
		"January 02, 2006",
		"01-02-2006",
		"1-2-2006",
		"1/2/06",
		"01/02/06",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized date format: %q", s)
}

func processAccountingPeriodsCSV(ctx context.Context, db *sql.DB, r io.Reader) (*csvUploadResult, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	cr.FieldsPerRecord = -1

	var headers []string
	colIdx := map[string]int{
		"year": -1, "periodnum": -1, "periodkey": -1,
		"monthname": -1, "startdate": -1, "enddate": -1,
		"weeks": -1, "days": -1,
	}

	lineNum := 0
	// Scan up to 20 initial rows to locate the real header row, skipping title/metadata banners
	for lineNum < 20 {
		lineNum++
		row, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}

		cleanRow := make([]string, len(row))
		curCol := map[string]int{
			"year": -1, "periodnum": -1, "periodkey": -1,
			"monthname": -1, "startdate": -1, "enddate": -1,
			"weeks": -1, "days": -1,
		}
		for i, h := range row {
			clean := strings.ToLower(strings.TrimSpace(h))
			cleanRow[i] = clean
			switch {
			case clean == "year" || clean == "fiscal year":
				curCol["year"] = i
			case clean == "period number" || clean == "period_number" || clean == "period #" || clean == "period" || clean == "period num":
				curCol["periodnum"] = i
			case clean == "period key" || clean == "period_key" || clean == "periodkey" || clean == "month (yyyy-mm)":
				curCol["periodkey"] = i
			case clean == "month name" || clean == "month_name" || clean == "month":
				curCol["monthname"] = i
			case strings.Contains(clean, "start"):
				curCol["startdate"] = i
			case strings.Contains(clean, "end"):
				curCol["enddate"] = i
			case strings.Contains(clean, "week"):
				curCol["weeks"] = i
			case strings.Contains(clean, "day"):
				curCol["days"] = i
			}
		}

		if curCol["startdate"] != -1 && curCol["enddate"] != -1 {
			headers = cleanRow
			colIdx = curCol
			break
		}
	}

	if colIdx["startdate"] == -1 || colIdx["enddate"] == -1 {
		return nil, fmt.Errorf("CSV must contain at least Start Date and End Date columns (got: %v)", strings.Join(headers, ", "))
	}

	result := &csvUploadResult{Errors: []string{}}

	for {
		lineNum++
		record, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("line %d: CSV read error: %v", lineNum, err))
			result.Skipped++
			continue
		}

		get := func(key string) string {
			idx := colIdx[key]
			if idx < 0 || idx >= len(record) {
				return ""
			}
			return strings.TrimSpace(record[idx])
		}

		sDateStr := get("startdate")
		eDateStr := get("enddate")
		if sDateStr == "" || eDateStr == "" {
			result.Skipped++
			continue
		}

		sDate, err := parseFlexibleDate(sDateStr)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("line %d: invalid start date %q: %v", lineNum, sDateStr, err))
			result.Skipped++
			continue
		}
		eDate, err := parseFlexibleDate(eDateStr)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("line %d: invalid end date %q: %v", lineNum, eDateStr, err))
			result.Skipped++
			continue
		}

		year := sDate.Year()
		if yStr := get("year"); yStr != "" {
			if y, err := strconv.Atoi(yStr); err == nil && y > 2000 {
				year = y
			}
		}

		periodKey := get("periodkey")
		if periodKey == "" {
			periodKey = fmt.Sprintf("%04d-%02d", year, int(sDate.Month()))
		}

		periodNum := int(sDate.Month())
		if pStr := get("periodnum"); pStr != "" {
			if pn, err := strconv.Atoi(pStr); err == nil && pn >= 1 && pn <= 13 {
				periodNum = pn
			}
		}

		monthName := get("monthname")
		if monthName == "" {
			monthName = sDate.Month().String()
		}

		days := int(eDate.Sub(sDate).Hours()/24) + 1
		if dStr := get("days"); dStr != "" {
			if d, err := strconv.Atoi(dStr); err == nil && d > 0 {
				days = d
			}
		}

		weeks := 4
		if days >= 32 {
			weeks = 5
		}
		if wStr := get("weeks"); wStr != "" {
			if w, err := strconv.Atoi(wStr); err == nil && (w == 4 || w == 5) {
				weeks = w
			}
		}

		period := &models.AccountingPeriod{
			Year:         year,
			PeriodNumber: periodNum,
			PeriodKey:    periodKey,
			MonthName:    monthName,
			StartDate:    sDate.Format("2006-01-02"),
			EndDate:      eDate.Format("2006-01-02"),
			WeeksCount:   weeks,
			DaysCount:    days,
		}

		if err := repositories.UpsertAccountingPeriod(ctx, db, period); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("line %d: DB error: %v", lineNum, err))
			result.Skipped++
			continue
		}

		result.Imported++
	}

	return result, nil
}
