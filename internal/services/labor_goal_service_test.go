package services

import (
	"testing"
	"time"
)

func TestLaborCalculations(t *testing.T) {
	// Test Allowed Budget Hours: Guests / Target LE
	// 540 guests / 2.10 FOH Target = 257.14 hours
	guests := 540
	fohTarget := 2.10
	budget := round2(float64(guests) / fohTarget)
	if budget != 257.14 {
		t.Fatalf("expected budget 257.14, got %.2f", budget)
	}

	// 540 guests / 3.50 BOH Target = 154.29 hours
	bohTarget := 3.50
	bohBudget := round2(float64(guests) / bohTarget)
	if bohBudget != 154.29 {
		t.Fatalf("expected BOH budget 154.29, got %.2f", bohBudget)
	}

	// Test Remaining Hours: Budget - Scheduled
	scheduled := 100.0
	remaining := round2(budget - scheduled)
	if remaining != 157.14 {
		t.Fatalf("expected remaining 157.14, got %.2f", remaining)
	}

	// Test Date Formatting helper
	start := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 11, 29, 0, 0, 0, 0, time.UTC)
	formatted := formatDatesRange(start, end)
	if formatted != "Nov 23–Nov 29" {
		t.Fatalf("expected 'Nov 23–Nov 29', got %q", formatted)
	}
}
