package repositories

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"wedding-project/internal/models"
)

// GetAccountingPeriodForDate finds the 4-4-5 operational month containing the given date.
func GetAccountingPeriodForDate(ctx context.Context, db *sql.DB, targetDate time.Time) (*models.AccountingPeriod, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is nil")
	}

	dateStr := targetDate.Format("2006-01-02")
	query := `
		SELECT id, year, period_number, period_key, month_name, start_date, end_date, weeks_count, days_count, created_at
		FROM accounting_periods
		WHERE start_date <= $1::date AND end_date >= $1::date
		ORDER BY period_number ASC
		LIMIT 1;
	`

	var p models.AccountingPeriod
	var sDate, eDate time.Time
	err := db.QueryRowContext(ctx, query, dateStr).Scan(
		&p.ID,
		&p.Year,
		&p.PeriodNumber,
		&p.PeriodKey,
		&p.MonthName,
		&sDate,
		&eDate,
		&p.WeeksCount,
		&p.DaysCount,
		&p.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get accounting period for date %s: %w", dateStr, err)
	}

	p.StartDate = sDate.Format("2006-01-02")
	p.EndDate = eDate.Format("2006-01-02")
	return &p, nil
}

// GetAccountingPeriodsByYear returns all 12 operational periods for a given year.
func GetAccountingPeriodsByYear(ctx context.Context, db *sql.DB, year int) ([]models.AccountingPeriod, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is nil")
	}

	query := `
		SELECT id, year, period_number, period_key, month_name, start_date, end_date, weeks_count, days_count, created_at
		FROM accounting_periods
		WHERE year = $1
		ORDER BY period_number ASC;
	`

	rows, err := db.QueryContext(ctx, query, year)
	if err != nil {
		return nil, fmt.Errorf("get accounting periods by year %d: %w", year, err)
	}
	defer rows.Close()

	var periods []models.AccountingPeriod
	for rows.Next() {
		var p models.AccountingPeriod
		var sDate, eDate time.Time
		if err := rows.Scan(
			&p.ID,
			&p.Year,
			&p.PeriodNumber,
			&p.PeriodKey,
			&p.MonthName,
			&sDate,
			&eDate,
			&p.WeeksCount,
			&p.DaysCount,
			&p.CreatedAt,
		); err != nil {
			return nil, err
		}
		p.StartDate = sDate.Format("2006-01-02")
		p.EndDate = eDate.Format("2006-01-02")
		periods = append(periods, p)
	}

	return periods, rows.Err()
}

// GetLaborTarget retrieves target FOH/BOH values for a venue and period key.
func GetLaborTarget(ctx context.Context, db *sql.DB, venueID, periodKey string) (*models.LaborEfficiencyTarget, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is nil")
	}

	query := `
		SELECT id, venue_id, COALESCE(venue_number, ''), year, period_key, foh_target, boh_target
		FROM labor_efficiency_targets
		WHERE venue_id = $1 AND period_key = $2
		LIMIT 1;
	`

	var t models.LaborEfficiencyTarget
	err := db.QueryRowContext(ctx, query, venueID, periodKey).Scan(
		&t.ID,
		&t.VenueID,
		&t.VenueNumber,
		&t.Year,
		&t.PeriodKey,
		&t.FOHTarget,
		&t.BOHTarget,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get labor target (%s, %s): %w", venueID, periodKey, err)
	}

	return &t, nil
}

// GetLaborTargets returns targets with venue metadata, optionally filtered by venueID and region.
func GetLaborTargets(ctx context.Context, db *sql.DB, venueID, region string, year int) ([]models.LaborEfficiencyTarget, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is nil")
	}

	query := `
		SELECT
			t.id, t.venue_id, COALESCE(v.name, ''), COALESCE(v.region, ''),
			COALESCE(t.venue_number, v.venue_number, ''), t.year, t.period_key,
			t.foh_target, t.boh_target
		FROM labor_efficiency_targets t
		LEFT JOIN venues v ON v.id = t.venue_id
		WHERE t.year = $1
	`
	args := []any{year}
	argIdx := 2

	if venueID != "" && venueID != "all" {
		query += fmt.Sprintf(" AND t.venue_id = $%d", argIdx)
		args = append(args, venueID)
		argIdx++
	}

	cleanRegion := strings.TrimSpace(region)
	if cleanRegion != "" && !strings.EqualFold(cleanRegion, "all") && !strings.EqualFold(cleanRegion, "all regions") {
		if strings.EqualFold(cleanRegion, "ungrouped") {
			query += " AND (v.region IS NULL OR TRIM(v.region) = '')"
		} else {
			query += fmt.Sprintf(" AND TRIM(v.region) = $%d", argIdx)
			args = append(args, cleanRegion)
			argIdx++
		}
	}

	query += " ORDER BY v.region ASC, v.name ASC, t.period_key ASC;"

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("get labor targets (year %d, venue %s, region %s): %w", year, venueID, region, err)
	}
	defer rows.Close()

	var targets []models.LaborEfficiencyTarget
	for rows.Next() {
		var t models.LaborEfficiencyTarget
		if err := rows.Scan(
			&t.ID,
			&t.VenueID,
			&t.VenueName,
			&t.Region,
			&t.VenueNumber,
			&t.Year,
			&t.PeriodKey,
			&t.FOHTarget,
			&t.BOHTarget,
		); err != nil {
			return nil, err
		}
		targets = append(targets, t)
	}

	return targets, rows.Err()
}

// GetAllActiveVenues returns a list of all active venues with region and venue number.
func GetAllActiveVenues(ctx context.Context, db *sql.DB) ([]models.Venue, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is nil")
	}

	rows, err := db.QueryContext(ctx, `
		SELECT id, name, COALESCE(venue_number, ''), COALESCE(region, ''), COALESCE(timezone, 'America/Denver')
		FROM venues
		WHERE is_active = true
		ORDER BY region ASC, name ASC;
	`)
	if err != nil {
		return nil, fmt.Errorf("get all active venues: %w", err)
	}
	defer rows.Close()

	var venues []models.Venue
	for rows.Next() {
		var v models.Venue
		if err := rows.Scan(&v.ID, &v.Name, &v.VenueNumber, &v.Region, &v.Timezone); err != nil {
			return nil, err
		}
		v.IsActive = true
		venues = append(venues, v)
	}
	return venues, rows.Err()
}

// GetEventGuestCountAndCount sums guest_count and counts total events in a date range.
func GetEventGuestCountAndCount(ctx context.Context, db *sql.DB, venueID string, startDate, endDate time.Time) (int, int, error) {
	if db == nil {
		return 0, 0, nil
	}

	sDateStr := startDate.Format("2006-01-02")
	eDateStr := endDate.Format("2006-01-02")

	query := `
		SELECT COALESCE(SUM(guest_count), 0), COUNT(*)
		FROM events
		WHERE venue_id = $1
		  AND event_date >= $2::date AND event_date <= $3::date;
	`

	var totalGuests, totalEvents int
	err := db.QueryRowContext(ctx, query, venueID, sDateStr, eDateStr).Scan(&totalGuests, &totalEvents)
	if err != nil {
		return 0, 0, fmt.Errorf("get guests in range (%s to %s): %w", sDateStr, eDateStr, err)
	}

	return totalGuests, totalEvents, nil
}

// UpsertLaborTarget inserts or updates a labor efficiency target row.
// Conflict key: (venue_id, period_key). On conflict, all mutable fields are overwritten.
func UpsertLaborTarget(ctx context.Context, db *sql.DB, t *models.LaborEfficiencyTarget) error {
	if db == nil {
		return fmt.Errorf("database connection is nil")
	}

	_, err := db.ExecContext(ctx, `
		INSERT INTO labor_efficiency_targets
			(venue_id, venue_number, year, period_key, foh_target, boh_target)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (venue_id, period_key) DO UPDATE SET
			venue_number = EXCLUDED.venue_number,
			year         = EXCLUDED.year,
			foh_target   = EXCLUDED.foh_target,
			boh_target   = EXCLUDED.boh_target;
	`,
		t.VenueID,
		nilIfEmpty(t.VenueNumber),
		t.Year,
		t.PeriodKey,
		t.FOHTarget,
		t.BOHTarget,
	)
	if err != nil {
		return fmt.Errorf("upsert labor target (venue=%s period=%s): %w", t.VenueID, t.PeriodKey, err)
	}
	return nil
}

// LookupVenueIDByName returns the venues.id (Salesforce Account ID) matching the given
// venue name via case-insensitive, trimmed comparison. Returns ("", nil) if not found.
func LookupVenueIDByName(ctx context.Context, db *sql.DB, name string) (string, error) {
	if db == nil {
		return "", fmt.Errorf("database connection is nil")
	}

	var id string
	err := db.QueryRowContext(ctx, `
		SELECT id FROM venues
		WHERE LOWER(TRIM(name)) = LOWER(TRIM($1))
		  AND is_active = true
		ORDER BY id
		LIMIT 1;
	`, name).Scan(&id)

	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("lookup venue by name %q: %w", name, err)
	}
	return id, nil
}

// LookupVenueIDByNumber returns the venues.id (Salesforce Account ID) matching the given
// venue number (e.g. "328", "214"). Returns ("", nil) if not found.
func LookupVenueIDByNumber(ctx context.Context, db *sql.DB, venueNum string) (string, error) {
	if db == nil || strings.TrimSpace(venueNum) == "" {
		return "", nil
	}

	var id string
	err := db.QueryRowContext(ctx, `
		SELECT id FROM venues
		WHERE TRIM(venue_number) = TRIM($1)
		  AND is_active = true
		ORDER BY id
		LIMIT 1;
	`, venueNum).Scan(&id)

	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("lookup venue by number %q: %w", venueNum, err)
	}
	return id, nil
}

// UpdateVenueMetadata updates the venue_number and/or region on an existing venue record.
// If clearRegion is true, region is set to NULL (unassigned). Otherwise, if region is non-empty,
// it updates region; if empty, existing region is preserved.
func UpdateVenueMetadata(ctx context.Context, db *sql.DB, venueID, venueNum, region string, clearRegion bool) error {
	if db == nil || venueID == "" {
		return nil
	}

	if clearRegion {
		_, err := db.ExecContext(ctx, `
			UPDATE venues
			SET venue_number = COALESCE(NULLIF($2, ''), venue_number),
			    region       = NULL,
			    updated_at   = NOW()
			WHERE id = $1;
		`, venueID, venueNum)
		return err
	}

	_, err := db.ExecContext(ctx, `
		UPDATE venues
		SET venue_number = COALESCE(NULLIF($2, ''), venue_number),
		    region       = COALESCE(NULLIF($3, ''), region),
		    updated_at   = NOW()
		WHERE id = $1;
	`, venueID, venueNum, region)
	return err
}

// nilIfEmpty converts an empty string to nil for nullable VARCHAR columns.
func nilIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// UpsertAccountingPeriod inserts or updates a 4-4-5 operational month row.
// Conflict key: period_key. On conflict, all calendar dates and metadata are updated.
func UpsertAccountingPeriod(ctx context.Context, db *sql.DB, p *models.AccountingPeriod) error {
	if db == nil {
		return fmt.Errorf("database connection is nil")
	}

	_, err := db.ExecContext(ctx, `
		INSERT INTO accounting_periods
			(year, period_number, period_key, month_name, start_date, end_date, weeks_count, days_count)
		VALUES ($1, $2, $3, $4, $5::date, $6::date, $7, $8)
		ON CONFLICT (period_key) DO UPDATE SET
			year          = EXCLUDED.year,
			period_number = EXCLUDED.period_number,
			month_name    = EXCLUDED.month_name,
			start_date    = EXCLUDED.start_date,
			end_date      = EXCLUDED.end_date,
			weeks_count   = EXCLUDED.weeks_count,
			days_count    = EXCLUDED.days_count;
	`,
		p.Year,
		p.PeriodNumber,
		p.PeriodKey,
		p.MonthName,
		p.StartDate,
		p.EndDate,
		p.WeeksCount,
		p.DaysCount,
	)
	if err != nil {
		return fmt.Errorf("upsert accounting period (%s): %w", p.PeriodKey, err)
	}
	return nil
}

