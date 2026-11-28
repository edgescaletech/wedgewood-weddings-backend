package repositories

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
	"wedding-project/internal/models"
)

// formatWeekRange formats Monday and Sunday into "Nov 23 – Nov 29, 2026".
func formatWeekRange(monday, sunday time.Time) string {
	if monday.Year() == sunday.Year() {
		return fmt.Sprintf("%s %d – %s %d, %d", monday.Format("Jan"), monday.Day(), sunday.Format("Jan"), sunday.Day(), monday.Year())
	}
	return fmt.Sprintf("%s %d, %d – %s %d, %d", monday.Format("Jan"), monday.Day(), monday.Year(), sunday.Format("Jan"), sunday.Day(), sunday.Year())
}

// formatMonthLabel formats a period into "November 2026 · Nov 2 – Nov 29".
func formatMonthLabel(monthName string, year int, startStr, endStr string) string {
	sDate, err1 := time.Parse("2006-01-02", startStr)
	eDate, err2 := time.Parse("2006-01-02", endStr)
	if err1 != nil || err2 != nil {
		return fmt.Sprintf("%s %d", monthName, year)
	}
	return fmt.Sprintf("%s %d · %s %d – %s %d", monthName, year, sDate.Format("Jan"), sDate.Day(), eDate.Format("Jan"), eDate.Day())
}

// GetVenuesOverview queries venues and joins labor efficiency targets for a specific week and month.
func GetVenuesOverview(ctx context.Context, db *sql.DB, targetDate time.Time, regionFilter, searchQuery string) (*models.VenuesOverviewResponse, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is nil")
	}

	// 1. Calculate week boundaries (Monday to Sunday)
	wd := int(targetDate.Weekday())
	if wd == 0 {
		wd = 7
	}
	monday := targetDate.AddDate(0, 0, -(wd - 1))
	monday = time.Date(monday.Year(), monday.Month(), monday.Day(), 0, 0, 0, 0, targetDate.Location())
	sunday := monday.AddDate(0, 0, 6)

	weekDatesLabel := formatWeekRange(monday, sunday)

	// 2. Lookup 4-4-5 operational month
	period, err := GetAccountingPeriodForDate(ctx, db, monday)
	if err != nil {
		return nil, fmt.Errorf("lookup accounting period: %w", err)
	}

	periodKey := fmt.Sprintf("%04d-%02d", monday.Year(), int(monday.Month()))
	monthLabel := fmt.Sprintf("%s %d", monday.Month().String(), monday.Year())

	if period != nil {
		periodKey = period.PeriodKey
		monthLabel = formatMonthLabel(period.MonthName, period.Year, period.StartDate, period.EndDate)
	}

	// 3. Query venues LEFT JOIN labor_efficiency_targets for this period
	baseQuery := `
		SELECT
			v.id,
			v.name,
			COALESCE(v.venue_number, ''),
			COALESCE(NULLIF(TRIM(v.region), ''), 'Ungrouped') AS r_name,
			t.foh_target,
			t.boh_target
		FROM venues v
		LEFT JOIN labor_efficiency_targets t
			ON t.venue_id = v.id AND t.period_key = $1
		WHERE v.is_active = true
	`
	args := []any{periodKey}
	argIdx := 2

	cleanRegion := strings.TrimSpace(regionFilter)
	if cleanRegion != "" && !strings.EqualFold(cleanRegion, "all") && !strings.EqualFold(cleanRegion, "all regions") {
		if strings.EqualFold(cleanRegion, "ungrouped") {
			baseQuery += " AND (v.region IS NULL OR TRIM(v.region) = '')"
		} else {
			baseQuery += fmt.Sprintf(" AND TRIM(v.region) = $%d", argIdx)
			args = append(args, cleanRegion)
			argIdx++
		}
	}

	cleanSearch := strings.TrimSpace(searchQuery)
	if cleanSearch != "" {
		searchPattern := "%" + strings.ToLower(cleanSearch) + "%"
		baseQuery += fmt.Sprintf(" AND (LOWER(v.name) LIKE $%d OR LOWER(COALESCE(v.venue_number, '')) LIKE $%d)", argIdx, argIdx)
		args = append(args, searchPattern)
		argIdx++
	}

	baseQuery += " ORDER BY r_name ASC, v.name ASC;"

	rows, err := db.QueryContext(ctx, baseQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("query venues overview: %w", err)
	}
	defer rows.Close()

	var items []models.VenueOverviewItem
	for rows.Next() {
		var item models.VenueOverviewItem
		var foh, boh *float64
		if err := rows.Scan(
			&item.ID,
			&item.Name,
			&item.VenueNumber,
			&item.Region,
			&foh,
			&boh,
		); err != nil {
			return nil, err
		}

		hasTarget := (foh != nil || boh != nil)
		targetMetric := models.VenueTargetMetric{
			FOHTarget: foh,
			BOHTarget: boh,
			HasTarget: hasTarget,
		}

		item.WeeklyPlan = targetMetric
		item.MonthlyPlan = targetMetric
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if items == nil {
		items = []models.VenueOverviewItem{}
	}

	return &models.VenuesOverviewResponse{
		WeekDates:   weekDatesLabel,
		MonthLabel:  monthLabel,
		PeriodKey:   periodKey,
		Venues:      items,
		TotalVenues: len(items),
	}, nil
}

// GetRegionSummaries returns all distinct reporting regions with their venue counts.
func GetRegionSummaries(ctx context.Context, db *sql.DB) ([]models.RegionSummary, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is nil")
	}

	query := `
		SELECT
			COALESCE(NULLIF(TRIM(region), ''), 'Ungrouped') AS r_name,
			COUNT(*) AS venue_count
		FROM venues
		WHERE is_active = true
		GROUP BY r_name
		ORDER BY r_name ASC;
	`

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("get region summaries: %w", err)
	}
	defer rows.Close()

	var summaries []models.RegionSummary
	for rows.Next() {
		var s models.RegionSummary
		if err := rows.Scan(&s.Name, &s.VenueCount); err != nil {
			return nil, err
		}
		summaries = append(summaries, s)
	}

	if summaries == nil {
		summaries = []models.RegionSummary{}
	}
	return summaries, rows.Err()
}

// BulkUpdateVenueRegion assigns the specified region to the given venue IDs and returns the number of rows updated.
func BulkUpdateVenueRegion(ctx context.Context, db *sql.DB, regionName string, venueIDs []string) (int64, error) {
	if db == nil {
		return 0, fmt.Errorf("database connection is nil")
	}
	if len(venueIDs) == 0 {
		return 0, nil
	}

	// Deduplicate venue IDs
	uniqueMap := make(map[string]struct{}, len(venueIDs))
	dedupedIDs := make([]string, 0, len(venueIDs))
	for _, id := range venueIDs {
		trimmedID := strings.TrimSpace(id)
		if trimmedID == "" {
			continue
		}
		if _, exists := uniqueMap[trimmedID]; !exists {
			uniqueMap[trimmedID] = struct{}{}
			dedupedIDs = append(dedupedIDs, trimmedID)
		}
	}

	if len(dedupedIDs) == 0 {
		return 0, nil
	}

	trimmedRegion := strings.TrimSpace(regionName)
	query := `
		UPDATE venues
		SET region = $1, updated_at = NOW()
		WHERE id = ANY($2);
	`

	res, err := db.ExecContext(ctx, query, trimmedRegion, pq.Array(dedupedIDs))
	if err != nil {
		return 0, fmt.Errorf("bulk update venue region to %q: %w", trimmedRegion, err)
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("get rows affected: %w", err)
	}
	return rowsAffected, nil
}
