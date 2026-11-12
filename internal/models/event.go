package models

// QuoteDetails holds rich catering, bar, and rental details from CPQ Quote and Quote Lines.
type QuoteDetails struct {
	QuoteID               string   `json:"quoteId,omitempty"`
	QuoteNumber           string   `json:"quoteNumber,omitempty"`
	IsPrimary             bool     `json:"isPrimary"`
	MealService           string   `json:"mealService,omitempty"`     // e.g. "Buffet Service" or "Plated Service"
	MealPackage           string   `json:"mealPackage,omitempty"`     // e.g. "Three Entree Buffet" (POS: Banquet Food)
	MealPackageQty        float64  `json:"mealPackageQty,omitempty"`  // Meal package covers
	BarPackage            string   `json:"barPackage,omitempty"`      // Primary bar package
	HostedBarType         string   `json:"hostedBarType,omitempty"`   // POS: Hosted Bar (Prints=false)
	HostedBarHours        *float64 `json:"hostedBarHours,omitempty"`  // # of Hosted Bar Hours; nil means Salesforce has no value
	NoHostBar             string   `json:"noHostBar,omitempty"`       // POS: No Host Bar
	Champagne             string   `json:"champagne,omitempty"`       // Champagne / toast product
	ChampagneQty          float64  `json:"champagneQty,omitempty"`    // Champagne quantity
	Wine                  string   `json:"wine,omitempty"`            // Hosted wine product
	WineQty               float64  `json:"wineQty,omitempty"`         // Wine quantity
	WineGlassesNeeded     bool     `json:"wineGlassesNeeded"`         // Inferred table settings
	ChampagneFlutesNeeded bool     `json:"champagneFlutesNeeded"`     // Inferred table settings
	LateNightSnacks       []string `json:"lateNightSnacks,omitempty"` // Service_Offerings__c: Munchies
	LoveLetters           string   `json:"loveLetters,omitempty"`     // Love Letters
	LoveLettersQty        float64  `json:"loveLettersQty,omitempty"`  // Love Letters quantity
	BarItems              []string `json:"barItems,omitempty"`        // Champagne toast, wine with dinner, etc.
	Appetizers            []string `json:"appetizers,omitempty"`      // Chicken & Waffles, Mini Crab Cakes, etc.
	Entrees               []string `json:"entrees,omitempty"`         // Panko Parmesan Chicken, Carved Roast Beef, etc.
	SidesSalads           []string `json:"sidesSalads,omitempty"`     // Garlic Mashed Potatoes, Seasonal Vegetables, etc.
	Dessert               string   `json:"dessert,omitempty"`         // Personalized Wedding Cake
	Linens                []string `json:"linens,omitempty"`          // e.g. ["Linen, Elite Full Length", "Linen, House"]
	Napkins               string   `json:"napkins,omitempty"`         // e.g. "Napkins, House"
	Chairs                string   `json:"chairs,omitempty"`          // e.g. "Chairs, Elite"
	DecorRentals          []string `json:"decorRentals,omitempty"`    // Floral Centerpieces, Charger Plates, Lawn Games, etc.
}

// Event represents an event record across the entire platform.
type Event struct {
	ID                        string        `json:"id"`
	Name                      string        `json:"name"`
	EventDate                 string        `json:"eventDate"`              // YYYY-MM-DD
	DayOfWeek                 string        `json:"dayOfWeek"`              // Friday, Saturday, etc.
	StartTime                 string        `json:"startTime"`              // e.g. 4:00 PM
	EndTime                   string        `json:"endTime"`                // e.g. 11:00 PM
	EventType                 string        `json:"eventType"`              // Wedding, Reception, etc.
	GuestCount                int           `json:"guestCount"`             // numeric covers
	Under21Count              *float64      `json:"under21Count,omitempty"` // Under-21 guests from Salesforce formula
	Over21Count               *float64      `json:"over21Count,omitempty"`  // Over-21 guests from Salesforce formula
	CeremonyStartTime         string        `json:"ceremonyStartTime,omitempty"`
	CeremonyStartDateTime     string        `json:"ceremonyStartDateTime,omitempty"`
	MealServiceTime           string        `json:"mealServiceTime,omitempty"`
	MealServiceDateTime       string        `json:"mealServiceDateTime,omitempty"`
	CocktailHourStartTime     string        `json:"cocktailHourStartTime,omitempty"`
	CocktailHourStartDateTime string        `json:"cocktailHourStartDateTime,omitempty"`
	BarStartTime              string        `json:"barStartTime,omitempty"`
	BarStartDateTime          string        `json:"barStartDateTime,omitempty"`
	BarCloseTime              string        `json:"barCloseTime,omitempty"`
	BarCloseDateTime          string        `json:"barCloseDateTime,omitempty"`
	BarLastCallTime           string        `json:"barLastCallTime,omitempty"`
	BarLastCallDateTime       string        `json:"barLastCallDateTime,omitempty"`
	Venue                     string        `json:"venue"`                            // Main venue/property name
	VenueID                   string        `json:"venueId,omitempty"`                // Venue Account ID
	VenueProperty             string        `json:"venueProperty"`                    // Property/estate name
	VenueGM                   string        `json:"venueGM,omitempty"`                // Venue General Manager
	CeremonyLocation          string        `json:"ceremonyLocation"`                 // Sub-place: e.g. Lakeside Arbor
	PhotoBooth                *bool         `json:"photoBooth"`                       // nil means Salesforce did not provide a value
	PhotoboothStaffed         bool          `json:"photoboothStaffed"`                // Venue-staffed photo booth
	HasBar                    bool          `json:"hasBar"`                           // bar service
	BarLocation               string        `json:"barLocation"`                      // Sub-place: e.g. Terrace Cocktail Patio
	AnticipatedBarActivity    string        `json:"anticipatedBarActivity,omitempty"` // Salesforce planning detail
	OtherBarNotes             string        `json:"otherBarNotes,omitempty"`
	BeverageCartDetails       string        `json:"beverageCartDetails,omitempty"`
	SignatureDrinks           string        `json:"signatureDrinks,omitempty"` // Specialty Drinks from Planning Details
	ChargerPlates             string        `json:"chargerPlates,omitempty"`   // Charger plates from Planning Details
	Notes                     string        `json:"notes"`                     // notes & special requests
	StageName                 string        `json:"stageName"`                 // stage
	Package                   string        `json:"package"`                   // Package tier (Elite, Premier, etc.)
	Coordinator               string        `json:"coordinator"`               // Day-of coordinator name
	DJName                    string        `json:"djName"`                    // DJ Name
	DecorSetupTime            string        `json:"decorSetupTime"`            // Decor setup time
	BEONotes                  string        `json:"beoNotes"`                  // Staff Facing BEO Notes
	ContractSigned            bool          `json:"contractSigned"`            // Contract signed status
	Quote                     *QuoteDetails `json:"quote,omitempty"`           // Level 2 & 3: CPQ Quote & QuoteLine details
}
