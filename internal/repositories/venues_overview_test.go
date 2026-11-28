package repositories

import (
	"testing"
	"time"
)

func TestFormatWeekRange(t *testing.T) {
	tests := []struct {
		name     string
		monday   time.Time
		sunday   time.Time
		expected string
	}{
		{
			name:     "same month and year",
			monday:   time.Date(2026, time.November, 23, 0, 0, 0, 0, time.UTC),
			sunday:   time.Date(2026, time.November, 29, 0, 0, 0, 0, time.UTC),
			expected: "Nov 23 – Nov 29, 2026",
		},
		{
			name:     "across month boundary within same year",
			monday:   time.Date(2026, time.October, 26, 0, 0, 0, 0, time.UTC),
			sunday:   time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC),
			expected: "Oct 26 – Nov 1, 2026",
		},
		{
			name:     "across year boundary",
			monday:   time.Date(2026, time.December, 28, 0, 0, 0, 0, time.UTC),
			sunday:   time.Date(2027, time.January, 3, 0, 0, 0, 0, time.UTC),
			expected: "Dec 28, 2026 – Jan 3, 2027",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatWeekRange(tc.monday, tc.sunday)
			if got != tc.expected {
				t.Errorf("formatWeekRange() = %q; want %q", got, tc.expected)
			}
		})
	}
}

func TestFormatMonthLabel(t *testing.T) {
	tests := []struct {
		name      string
		monthName string
		year      int
		startStr  string
		endStr    string
		expected  string
	}{
		{
			name:      "valid start and end dates",
			monthName: "October",
			year:      2026,
			startStr:  "2026-10-05",
			endStr:    "2026-11-01",
			expected:  "October 2026 · Oct 5 – Nov 1",
		},
		{
			name:      "invalid start date falls back gracefully",
			monthName: "November",
			year:      2026,
			startStr:  "invalid-date",
			endStr:    "2026-11-29",
			expected:  "November 2026",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatMonthLabel(tc.monthName, tc.year, tc.startStr, tc.endStr)
			if got != tc.expected {
				t.Errorf("formatMonthLabel() = %q; want %q", got, tc.expected)
			}
		})
	}
}
