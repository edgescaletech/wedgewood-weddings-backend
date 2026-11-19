package models

import "time"

// Venue represents a property location hosting events.
type Venue struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	VenueNumber    string    `json:"venueNumber,omitempty"`
	Region         string    `json:"region,omitempty"`
	PropertyName   string    `json:"propertyName,omitempty"`
	GeneralManager string    `json:"generalManager,omitempty"`
	Timezone       string    `json:"timezone"`
	IsActive       bool      `json:"isActive"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}
