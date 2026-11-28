package models

import "time"

// AccountingPeriod represents a single 4-4-5 Wedgewood operational month.
type AccountingPeriod struct {
	ID           string    `json:"id"`
	Year         int       `json:"year"`
	PeriodNumber int       `json:"periodNumber"`
	PeriodKey    string    `json:"periodKey"`   // e.g. "2026-11"
	MonthName    string    `json:"monthName"`   // e.g. "November"
	StartDate    string    `json:"startDate"`   // "YYYY-MM-DD"
	EndDate      string    `json:"endDate"`     // "YYYY-MM-DD"
	WeeksCount   int       `json:"weeksCount"`  // 4 or 5
	DaysCount    int       `json:"daysCount"`   // 28, 31, 32, 35
	CreatedAt    time.Time `json:"createdAt"`
}

// LaborEfficiencyTarget represents monthly FOH/BOH targets for a venue.
type LaborEfficiencyTarget struct {
	ID          string   `json:"id"`
	VenueID     string   `json:"venueId"`
	VenueName   string   `json:"venueName,omitempty"`
	Region      string   `json:"region,omitempty"`
	VenueNumber string   `json:"venueNumber,omitempty"`
	Year        int      `json:"year"`
	PeriodKey   string   `json:"periodKey"`
	FOHTarget   *float64 `json:"fohTarget"`
	BOHTarget   *float64 `json:"bohTarget"`
}

// DepartmentMetric contains calculated efficiency, budget, and scheduled hours.
type DepartmentMetric struct {
	TargetLE           float64 `json:"targetLe"`
	CurrentLE          float64 `json:"currentLe"`
	BudgetHours        float64 `json:"budgetHours"`
	ScheduledHours     float64 `json:"scheduledHours"`
	RemainingHours     float64 `json:"remainingHours"`
	TotalGuests        int     `json:"totalGuests"`
	TotalEvents        int     `json:"totalEvents"`
	EventsWithStaffing int     `json:"eventsWithStaffing"`
}

// PeriodLaborPlan contains metrics for a specific planning window (week or month).
type PeriodLaborPlan struct {
	Label string           `json:"label"`
	Dates string           `json:"dates"`
	FOH   DepartmentMetric `json:"foh"`
	BOH   DepartmentMetric `json:"boh"`
}

// LaborGoalsSummaryResponse represents the complete response for the Labor Goals Accordion.
type LaborGoalsSummaryResponse struct {
	VenueID     string          `json:"venueId"`
	VenueName   string          `json:"venueName"`
	WeekPlan    PeriodLaborPlan `json:"weekPlan"`
	MonthPlan   PeriodLaborPlan `json:"monthPlan"`
	PeriodKey   string          `json:"periodKey"`
	HasLiveGoal bool            `json:"hasLiveGoal"`
}
