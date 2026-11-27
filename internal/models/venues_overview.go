package models

// VenueTargetMetric holds target efficiency values for FOH and BOH.
type VenueTargetMetric struct {
	FOHTarget *float64 `json:"fohTarget"`
	BOHTarget *float64 `json:"bohTarget"`
	HasTarget bool     `json:"hasTarget"`
}

// VenueOverviewItem represents a venue with its targets for the Venues RM dashboard.
type VenueOverviewItem struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	VenueNumber string            `json:"venueNumber,omitempty"`
	Region      string            `json:"region"`
	WeeklyPlan  VenueTargetMetric `json:"weeklyPlan"`
	MonthlyPlan VenueTargetMetric `json:"monthlyPlan"`
}

// VenuesOverviewResponse represents the response containing venues and planning period metadata.
type VenuesOverviewResponse struct {
	WeekDates   string              `json:"weekDates"`  // e.g. "Nov 23 – Nov 29, 2026"
	MonthLabel  string              `json:"monthLabel"` // e.g. "November 2026 · Nov 2 – Nov 29"
	PeriodKey   string              `json:"periodKey"`  // e.g. "2026-11"
	Venues      []VenueOverviewItem `json:"venues"`
	TotalVenues int                 `json:"totalVenues"`
}

// RegionSummary represents a reporting region with its count of active venues.
type RegionSummary struct {
	Name       string `json:"name"`
	VenueCount int    `json:"venueCount"`
}

// UpdateRegionRequest represents the payload to update/create a reporting region.
type UpdateRegionRequest struct {
	RegionName string   `json:"regionName"`
	VenueIDs   []string `json:"venueIds"`
}
