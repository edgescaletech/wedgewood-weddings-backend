package repositories

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"wedding-project/internal/models"
)

// EventRepository manages database operations for events.
type EventRepository struct {
	db *sql.DB
}

// NewEventRepository creates a new EventRepository instance.
func NewEventRepository(db *sql.DB) *EventRepository {
	return &EventRepository{db: db}
}

// UpsertVenue inserts or updates a venue record.
func UpsertVenue(ctx context.Context, db *sql.DB, id, name, propertyName, gm string) error {
	if id == "" {
		return nil
	}
	query := `
INSERT INTO venues (id, name, property_name, general_manager, updated_at)
VALUES ($1, $2, $3, $4, NOW())
ON CONFLICT (id) DO UPDATE SET
	name = EXCLUDED.name,
	property_name = COALESCE(NULLIF(EXCLUDED.property_name, ''), venues.property_name),
	general_manager = COALESCE(NULLIF(EXCLUDED.general_manager, ''), venues.general_manager),
	updated_at = NOW();`
	_, err := db.ExecContext(ctx, query, id, name, propertyName, gm)
	return err
}

// UpsertEvent inserts or updates a single event record.
func UpsertEvent(ctx context.Context, db *sql.DB, evt *models.Event) error {
	if evt == nil || evt.ID == "" {
		return fmt.Errorf("invalid event: missing ID")
	}

	// Ensure venue exists first to satisfy foreign key constraint
	if evt.VenueID != "" {
		_ = UpsertVenue(ctx, db, evt.VenueID, evt.Venue, evt.VenueProperty, evt.VenueGM)
	}

	var cateringJSON []byte
	if evt.Quote != nil {
		cateringJSON, _ = json.Marshal(evt.Quote)
	}

	eventDate, err := time.Parse("2006-01-02", evt.EventDate)
	if err != nil {
		eventDate = time.Now()
	}

	query := `
INSERT INTO events (
	id, venue_id, name, event_date, day_of_week, start_time, end_time,
	event_type, guest_count, under_21_count, over_21_count, stage_name, contract_signed,
	venue_name, venue_property, venue_gm, ceremony_location,
	ceremony_start_time, ceremony_start_datetime, meal_service_time, meal_service_datetime,
	cocktail_hour_start_time, cocktail_hour_start_datetime,
	has_bar, bar_location, bar_start_time, bar_start_datetime, bar_close_time, bar_close_datetime,
	bar_last_call_time, bar_last_call_datetime, anticipated_bar_activity, other_bar_notes,
	beverage_cart_details, signature_drinks, charger_plates, photo_booth, photobooth_staffed,
	package_name, coordinator_name, dj_name, decor_setup_time, beo_notes, notes, catering_details, updated_at
) VALUES (
	$1, $2, $3, $4, $5, $6, $7,
	$8, $9, $10, $11, $12, $13,
	$14, $15, $16, $17,
	$18, $19, $20, $21,
	$22, $23,
	$24, $25, $26, $27, $28, $29,
	$30, $31, $32, $33,
	$34, $35, $36, $37, $38,
	$39, $40, $41, $42, $43, $44, $45, NOW()
)
ON CONFLICT (id) DO UPDATE SET
	venue_id = EXCLUDED.venue_id,
	name = EXCLUDED.name,
	event_date = EXCLUDED.event_date,
	day_of_week = EXCLUDED.day_of_week,
	start_time = EXCLUDED.start_time,
	end_time = EXCLUDED.end_time,
	event_type = EXCLUDED.event_type,
	guest_count = EXCLUDED.guest_count,
	under_21_count = EXCLUDED.under_21_count,
	over_21_count = EXCLUDED.over_21_count,
	stage_name = EXCLUDED.stage_name,
	contract_signed = EXCLUDED.contract_signed,
	venue_name = EXCLUDED.venue_name,
	venue_property = EXCLUDED.venue_property,
	venue_gm = EXCLUDED.venue_gm,
	ceremony_location = EXCLUDED.ceremony_location,
	ceremony_start_time = EXCLUDED.ceremony_start_time,
	ceremony_start_datetime = EXCLUDED.ceremony_start_datetime,
	meal_service_time = EXCLUDED.meal_service_time,
	meal_service_datetime = EXCLUDED.meal_service_datetime,
	cocktail_hour_start_time = EXCLUDED.cocktail_hour_start_time,
	cocktail_hour_start_datetime = EXCLUDED.cocktail_hour_start_datetime,
	has_bar = EXCLUDED.has_bar,
	bar_location = EXCLUDED.bar_location,
	bar_start_time = EXCLUDED.bar_start_time,
	bar_start_datetime = EXCLUDED.bar_start_datetime,
	bar_close_time = EXCLUDED.bar_close_time,
	bar_close_datetime = EXCLUDED.bar_close_datetime,
	bar_last_call_time = EXCLUDED.bar_last_call_time,
	bar_last_call_datetime = EXCLUDED.bar_last_call_datetime,
	anticipated_bar_activity = EXCLUDED.anticipated_bar_activity,
	other_bar_notes = EXCLUDED.other_bar_notes,
	beverage_cart_details = EXCLUDED.beverage_cart_details,
	signature_drinks = EXCLUDED.signature_drinks,
	charger_plates = EXCLUDED.charger_plates,
	photo_booth = EXCLUDED.photo_booth,
	photobooth_staffed = EXCLUDED.photobooth_staffed,
	package_name = EXCLUDED.package_name,
	coordinator_name = EXCLUDED.coordinator_name,
	dj_name = EXCLUDED.dj_name,
	decor_setup_time = EXCLUDED.decor_setup_time,
	beo_notes = EXCLUDED.beo_notes,
	notes = EXCLUDED.notes,
	catering_details = COALESCE(EXCLUDED.catering_details, events.catering_details),
	updated_at = NOW();`

	var venueIDParam *string
	if evt.VenueID != "" {
		venueIDParam = &evt.VenueID
	}

	var cateringParam any
	if len(cateringJSON) > 0 {
		cateringParam = cateringJSON
	}

	_, err = db.ExecContext(ctx, query,
		evt.ID, venueIDParam, evt.Name, eventDate, evt.DayOfWeek, evt.StartTime, evt.EndTime,
		evt.EventType, evt.GuestCount, evt.Under21Count, evt.Over21Count, evt.StageName, evt.ContractSigned,
		evt.Venue, evt.VenueProperty, evt.VenueGM, evt.CeremonyLocation,
		evt.CeremonyStartTime, evt.CeremonyStartDateTime, evt.MealServiceTime, evt.MealServiceDateTime,
		evt.CocktailHourStartTime, evt.CocktailHourStartDateTime,
		evt.HasBar, evt.BarLocation, evt.BarStartTime, evt.BarStartDateTime, evt.BarCloseTime, evt.BarCloseDateTime,
		evt.BarLastCallTime, evt.BarLastCallDateTime, evt.AnticipatedBarActivity, evt.OtherBarNotes,
		evt.BeverageCartDetails, evt.SignatureDrinks, evt.ChargerPlates, evt.PhotoBooth, evt.PhotoboothStaffed,
		evt.Package, evt.Coordinator, evt.DJName, evt.DecorSetupTime, evt.BEONotes, evt.Notes, cateringParam,
	)
	return err
}

// UpsertEvents writes a batch of events inside a single transaction.
func UpsertEvents(ctx context.Context, db *sql.DB, evts []models.Event) error {
	if len(evts) == 0 {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	venueStmt, err := tx.PrepareContext(ctx, `
INSERT INTO venues (id, name, property_name, general_manager, updated_at)
VALUES ($1, $2, $3, $4, NOW())
ON CONFLICT (id) DO UPDATE SET
	name = EXCLUDED.name,
	property_name = COALESCE(NULLIF(EXCLUDED.property_name, ''), venues.property_name),
	general_manager = COALESCE(NULLIF(EXCLUDED.general_manager, ''), venues.general_manager),
	updated_at = NOW();`)
	if err != nil {
		return err
	}
	defer venueStmt.Close()

	for _, e := range evts {
		if e.VenueID != "" {
			_, _ = venueStmt.ExecContext(ctx, e.VenueID, e.Venue, e.VenueProperty, e.VenueGM)
		}
	}

	eventStmt, err := tx.PrepareContext(ctx, `
INSERT INTO events (
	id, venue_id, name, event_date, day_of_week, start_time, end_time,
	event_type, guest_count, under_21_count, over_21_count, stage_name, contract_signed,
	venue_name, venue_property, venue_gm, ceremony_location,
	ceremony_start_time, ceremony_start_datetime, meal_service_time, meal_service_datetime,
	cocktail_hour_start_time, cocktail_hour_start_datetime,
	has_bar, bar_location, bar_start_time, bar_start_datetime, bar_close_time, bar_close_datetime,
	bar_last_call_time, bar_last_call_datetime, anticipated_bar_activity, other_bar_notes,
	beverage_cart_details, signature_drinks, charger_plates, photo_booth, photobooth_staffed,
	package_name, coordinator_name, dj_name, decor_setup_time, beo_notes, notes, catering_details, updated_at
) VALUES (
	$1, $2, $3, $4, $5, $6, $7,
	$8, $9, $10, $11, $12, $13,
	$14, $15, $16, $17,
	$18, $19, $20, $21,
	$22, $23,
	$24, $25, $26, $27, $28, $29,
	$30, $31, $32, $33,
	$34, $35, $36, $37, $38,
	$39, $40, $41, $42, $43, $44, $45, NOW()
)
ON CONFLICT (id) DO UPDATE SET
	venue_id = EXCLUDED.venue_id,
	name = EXCLUDED.name,
	event_date = EXCLUDED.event_date,
	day_of_week = EXCLUDED.day_of_week,
	start_time = EXCLUDED.start_time,
	end_time = EXCLUDED.end_time,
	event_type = EXCLUDED.event_type,
	guest_count = EXCLUDED.guest_count,
	under_21_count = EXCLUDED.under_21_count,
	over_21_count = EXCLUDED.over_21_count,
	stage_name = EXCLUDED.stage_name,
	contract_signed = EXCLUDED.contract_signed,
	venue_name = EXCLUDED.venue_name,
	venue_property = EXCLUDED.venue_property,
	venue_gm = EXCLUDED.venue_gm,
	ceremony_location = EXCLUDED.ceremony_location,
	ceremony_start_time = EXCLUDED.ceremony_start_time,
	ceremony_start_datetime = EXCLUDED.ceremony_start_datetime,
	meal_service_time = EXCLUDED.meal_service_time,
	meal_service_datetime = EXCLUDED.meal_service_datetime,
	cocktail_hour_start_time = EXCLUDED.cocktail_hour_start_time,
	cocktail_hour_start_datetime = EXCLUDED.cocktail_hour_start_datetime,
	has_bar = EXCLUDED.has_bar,
	bar_location = EXCLUDED.bar_location,
	bar_start_time = EXCLUDED.bar_start_time,
	bar_start_datetime = EXCLUDED.bar_start_datetime,
	bar_close_time = EXCLUDED.bar_close_time,
	bar_close_datetime = EXCLUDED.bar_close_datetime,
	bar_last_call_time = EXCLUDED.bar_last_call_time,
	bar_last_call_datetime = EXCLUDED.bar_last_call_datetime,
	anticipated_bar_activity = EXCLUDED.anticipated_bar_activity,
	other_bar_notes = EXCLUDED.other_bar_notes,
	beverage_cart_details = EXCLUDED.beverage_cart_details,
	signature_drinks = EXCLUDED.signature_drinks,
	charger_plates = EXCLUDED.charger_plates,
	photo_booth = EXCLUDED.photo_booth,
	photobooth_staffed = EXCLUDED.photobooth_staffed,
	package_name = EXCLUDED.package_name,
	coordinator_name = EXCLUDED.coordinator_name,
	dj_name = EXCLUDED.dj_name,
	decor_setup_time = EXCLUDED.decor_setup_time,
	beo_notes = EXCLUDED.beo_notes,
	notes = EXCLUDED.notes,
	catering_details = COALESCE(EXCLUDED.catering_details, events.catering_details),
	updated_at = NOW();`)
	if err != nil {
		return err
	}
	defer eventStmt.Close()

	for _, evt := range evts {
		var cateringJSON []byte
		if evt.Quote != nil {
			cateringJSON, _ = json.Marshal(evt.Quote)
		}

		eventDate, err := time.Parse("2006-01-02", evt.EventDate)
		if err != nil {
			eventDate = time.Now()
		}

		var venueIDParam *string
		if evt.VenueID != "" {
			venueIDParam = &evt.VenueID
		}

		var cateringParam any
		if len(cateringJSON) > 0 {
			cateringParam = cateringJSON
		}

		_, err = eventStmt.ExecContext(ctx,
			evt.ID, venueIDParam, evt.Name, eventDate, evt.DayOfWeek, evt.StartTime, evt.EndTime,
			evt.EventType, evt.GuestCount, evt.Under21Count, evt.Over21Count, evt.StageName, evt.ContractSigned,
			evt.Venue, evt.VenueProperty, evt.VenueGM, evt.CeremonyLocation,
			evt.CeremonyStartTime, evt.CeremonyStartDateTime, evt.MealServiceTime, evt.MealServiceDateTime,
			evt.CocktailHourStartTime, evt.CocktailHourStartDateTime,
			evt.HasBar, evt.BarLocation, evt.BarStartTime, evt.BarStartDateTime, evt.BarCloseTime, evt.BarCloseDateTime,
			evt.BarLastCallTime, evt.BarLastCallDateTime, evt.AnticipatedBarActivity, evt.OtherBarNotes,
			evt.BeverageCartDetails, evt.SignatureDrinks, evt.ChargerPlates, evt.PhotoBooth, evt.PhotoboothStaffed,
			evt.Package, evt.Coordinator, evt.DJName, evt.DecorSetupTime, evt.BEONotes, evt.Notes, cateringParam,
		)
		if err != nil {
			return fmt.Errorf("upsert event %s: %w", evt.ID, err)
		}
	}

	return tx.Commit()
}

func scanEvent(rows *sql.Rows) (models.Event, error) {
	var e models.Event
	var eventDate time.Time
	var venueID, dayOfWeek, startTime, endTime, eventType sql.NullString
	var stageName, venueName, venueProperty, venueGM, ceremonyLocation sql.NullString
	var ceremonyStartTime, ceremonyStartDateTime, mealServiceTime, mealServiceDateTime sql.NullString
	var cocktailHourStartTime, cocktailHourStartDateTime sql.NullString
	var barLocation, barStartTime, barStartDateTime, barCloseTime, barCloseDateTime sql.NullString
	var barLastCallTime, barLastCallDateTime, anticipatedBarActivity, otherBarNotes sql.NullString
	var beverageCartDetails, signatureDrinks, chargerPlates sql.NullString
	var packageName, coordinatorName, djName, decorSetupTime, beoNotes, notes sql.NullString
	var cateringBytes []byte

	err := rows.Scan(
		&e.ID, &venueID, &e.Name, &eventDate, &dayOfWeek, &startTime, &endTime,
		&eventType, &e.GuestCount, &e.Under21Count, &e.Over21Count, &stageName, &e.ContractSigned,
		&venueName, &venueProperty, &venueGM, &ceremonyLocation,
		&ceremonyStartTime, &ceremonyStartDateTime, &mealServiceTime, &mealServiceDateTime,
		&cocktailHourStartTime, &cocktailHourStartDateTime,
		&e.HasBar, &barLocation, &barStartTime, &barStartDateTime, &barCloseTime, &barCloseDateTime,
		&barLastCallTime, &barLastCallDateTime, &anticipatedBarActivity, &otherBarNotes,
		&beverageCartDetails, &signatureDrinks, &chargerPlates, &e.PhotoBooth, &e.PhotoboothStaffed,
		&packageName, &coordinatorName, &djName, &decorSetupTime, &beoNotes, &notes, &cateringBytes,
	)
	if err != nil {
		return e, err
	}

	e.EventDate = eventDate.Format("2006-01-02")
	e.VenueID = venueID.String
	e.DayOfWeek = dayOfWeek.String
	e.StartTime = startTime.String
	e.EndTime = endTime.String
	e.EventType = eventType.String
	e.StageName = stageName.String
	e.Venue = venueName.String
	e.VenueProperty = venueProperty.String
	e.VenueGM = venueGM.String
	e.CeremonyLocation = ceremonyLocation.String
	e.CeremonyStartTime = ceremonyStartTime.String
	e.CeremonyStartDateTime = ceremonyStartDateTime.String
	e.MealServiceTime = mealServiceTime.String
	e.MealServiceDateTime = mealServiceDateTime.String
	e.CocktailHourStartTime = cocktailHourStartTime.String
	e.CocktailHourStartDateTime = cocktailHourStartDateTime.String
	e.BarLocation = barLocation.String
	e.BarStartTime = barStartTime.String
	e.BarStartDateTime = barStartDateTime.String
	e.BarCloseTime = barCloseTime.String
	e.BarCloseDateTime = barCloseDateTime.String
	e.BarLastCallTime = barLastCallTime.String
	e.BarLastCallDateTime = barLastCallDateTime.String
	e.AnticipatedBarActivity = anticipatedBarActivity.String
	e.OtherBarNotes = otherBarNotes.String
	e.BeverageCartDetails = beverageCartDetails.String
	e.SignatureDrinks = signatureDrinks.String
	e.ChargerPlates = chargerPlates.String
	e.Package = packageName.String
	e.Coordinator = coordinatorName.String
	e.DJName = djName.String
	e.DecorSetupTime = decorSetupTime.String
	e.BEONotes = beoNotes.String
	e.Notes = notes.String

	if len(cateringBytes) > 0 {
		var q models.QuoteDetails
		if err := json.Unmarshal(cateringBytes, &q); err == nil {
			e.Quote = &q
		}
	}

	return e, nil
}

const selectEventColumns = `
	id, venue_id, name, event_date, day_of_week, start_time, end_time,
	event_type, guest_count, under_21_count, over_21_count, stage_name, contract_signed,
	venue_name, venue_property, venue_gm, ceremony_location,
	ceremony_start_time, ceremony_start_datetime, meal_service_time, meal_service_datetime,
	cocktail_hour_start_time, cocktail_hour_start_datetime,
	has_bar, bar_location, bar_start_time, bar_start_datetime, bar_close_time, bar_close_datetime,
	bar_last_call_time, bar_last_call_datetime, anticipated_bar_activity, other_bar_notes,
	beverage_cart_details, signature_drinks, charger_plates, photo_booth, photobooth_staffed,
	package_name, coordinator_name, dj_name, decor_setup_time, beo_notes, notes, catering_details
`

// GetEventsByWeek fetches events for the given week from PostgreSQL.
func GetEventsByWeek(ctx context.Context, db *sql.DB, weekStart, weekEnd time.Time, venueID string) ([]models.Event, error) {
	var query string
	var args []any

	if venueID != "" {
		query = fmt.Sprintf(`
			SELECT %s
			FROM events
			WHERE event_date >= $1 AND event_date <= $2 AND venue_id = $3
			  AND stage_name NOT IN ('Closed Lost', 'Cancelled')
			ORDER BY event_date ASC, start_time ASC`, selectEventColumns)
		args = []any{weekStart.Format("2006-01-02"), weekEnd.Format("2006-01-02"), venueID}
	} else {
		query = fmt.Sprintf(`
			SELECT %s
			FROM events
			WHERE event_date >= $1 AND event_date <= $2
			  AND stage_name NOT IN ('Closed Lost', 'Cancelled')
			ORDER BY event_date ASC, start_time ASC`, selectEventColumns)
		args = []any{weekStart.Format("2006-01-02"), weekEnd.Format("2006-01-02")}
	}

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query weekly events: %w", err)
	}
	defer rows.Close()

	events := make([]models.Event, 0)
	for rows.Next() {
		evt, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		events = append(events, evt)
	}

	return events, rows.Err()
}

// GetEventByID fetches a single event from PostgreSQL by ID.
func GetEventByID(ctx context.Context, db *sql.DB, id string) (*models.Event, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM events
		WHERE id = $1
		LIMIT 1`, selectEventColumns)

	rows, err := db.QueryContext(ctx, query, id)
	if err != nil {
		return nil, fmt.Errorf("query event by id: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, sql.ErrNoRows
	}

	evt, err := scanEvent(rows)
	if err != nil {
		return nil, fmt.Errorf("scan event: %w", err)
	}
	return &evt, nil
}

// HasEventsForWeek checks if database already contains records for the requested date window.
func HasEventsForWeek(ctx context.Context, db *sql.DB, weekStart, weekEnd time.Time) (bool, error) {
	var count int
	query := `SELECT COUNT(*) FROM events WHERE event_date >= $1 AND event_date <= $2`
	err := db.QueryRowContext(ctx, query, weekStart.Format("2006-01-02"), weekEnd.Format("2006-01-02")).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// LogPubSubEvent writes an incoming CDC change event to integration_inbox for audit and tracking.
func LogPubSubEvent(ctx context.Context, db *sql.DB, entityName, changeType, sourceRecordID string, payload map[string]any) error {
	payloadBytes, _ := json.Marshal(payload)
	query := `
		INSERT INTO integration_inbox (entity_name, change_type, source_record_id, payload, status, received_at)
		VALUES ($1, $2, $3, $4, 'processed', NOW())`
	_, err := db.ExecContext(ctx, query, entityName, changeType, sourceRecordID, payloadBytes)
	return err
}
