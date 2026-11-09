package salesforce

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// QuoteDetails holds rich catering, bar, and rental details from CPQ Quote and Quote Lines.
type QuoteDetails struct {
	QuoteID               string   `json:"quoteId,omitempty"`
	QuoteNumber           string   `json:"quoteNumber,omitempty"`
	IsPrimary             bool     `json:"isPrimary"`
	MealService           string   `json:"mealService,omitempty"`           // e.g. "Buffet Service" or "Plated Service"
	MealPackage           string   `json:"mealPackage,omitempty"`           // e.g. "Three Entree Buffet" (POS: Banquet Food)
	MealPackageQty        float64  `json:"mealPackageQty,omitempty"`        // Meal package covers
	BarPackage            string   `json:"barPackage,omitempty"`            // Primary bar package
	HostedBarType         string   `json:"hostedBarType,omitempty"`         // POS: Hosted Bar (Prints=false)
	HostedBarHours        float64  `json:"hostedBarHours,omitempty"`        // # of Hosted Bar Hours
	NoHostBar             string   `json:"noHostBar,omitempty"`             // POS: No Host Bar
	Champagne             string   `json:"champagne,omitempty"`             // Champagne / toast product
	ChampagneQty          float64  `json:"champagneQty,omitempty"`          // Champagne quantity
	Wine                  string   `json:"wine,omitempty"`                  // Hosted wine product
	WineQty               float64  `json:"wineQty,omitempty"`               // Wine quantity
	WineGlassesNeeded     bool     `json:"wineGlassesNeeded"`               // Inferred table settings
	ChampagneFlutesNeeded bool     `json:"champagneFlutesNeeded"`           // Inferred table settings
	LateNightSnacks       []string `json:"lateNightSnacks,omitempty"`       // Service_Offerings__c: Munchies
	LoveLetters           string   `json:"loveLetters,omitempty"`           // Love Letters
	LoveLettersQty        float64  `json:"loveLettersQty,omitempty"`        // Love Letters quantity
	BarItems              []string `json:"barItems,omitempty"`              // Champagne toast, wine with dinner, etc.
	Appetizers            []string `json:"appetizers,omitempty"`            // Chicken & Waffles, Mini Crab Cakes, etc.
	Entrees               []string `json:"entrees,omitempty"`               // Panko Parmesan Chicken, Carved Roast Beef, etc.
	SidesSalads           []string `json:"sidesSalads,omitempty"`           // Garlic Mashed Potatoes, Seasonal Vegetables, etc.
	Dessert               string   `json:"dessert,omitempty"`               // Personalized Wedding Cake
	Linens                []string `json:"linens,omitempty"`                // e.g. ["Linen, Elite Full Length", "Linen, House"]
	Napkins               string   `json:"napkins,omitempty"`               // e.g. "Napkins, House"
	Chairs                string   `json:"chairs,omitempty"`                // e.g. "Chairs, Elite"
	DecorRentals          []string `json:"decorRentals,omitempty"`          // Floral Centerpieces, Charger Plates, Lawn Games, etc.
}

// Event maps the Salesforce Opportunity fields we display in the UI.
type Event struct {
	ID                string        `json:"id"`
	Name              string        `json:"name"`
	EventDate         string        `json:"eventDate"`        // YYYY-MM-DD
	DayOfWeek         string        `json:"dayOfWeek"`        // Friday, Saturday, etc.
	StartTime         string        `json:"startTime"`        // e.g. 4:00 PM
	EndTime           string        `json:"endTime"`          // e.g. 11:00 PM
	EventType         string        `json:"eventType"`        // Wedding, Reception, etc.
	GuestCount        int           `json:"guestCount"`       // numeric covers
	Venue             string        `json:"venue"`            // Main venue/property name
	VenueID           string        `json:"venueId,omitempty"`// Venue Account ID
	VenueProperty     string        `json:"venueProperty"`    // Property/estate name
	VenueGM           string        `json:"venueGM,omitempty"`// Venue General Manager
	CeremonyLocation  string        `json:"ceremonyLocation"` // Sub-place: e.g. Lakeside Arbor
	PhotoBooth        bool          `json:"photoBooth"`       // photo booth available
	PhotoboothStaffed bool          `json:"photoboothStaffed"`// Venue-staffed photo booth
	HasBar            bool          `json:"hasBar"`           // bar service
	BarLocation       string        `json:"barLocation"`      // Sub-place: e.g. Terrace Cocktail Patio
	SignatureDrinks   string        `json:"signatureDrinks,omitempty"` // Specialty Drinks from Planning Details
	ChargerPlates     string        `json:"chargerPlates,omitempty"`   // Charger plates from Planning Details
	Notes             string        `json:"notes"`            // notes & special requests
	StageName         string        `json:"stageName"`        // stage
	Package           string        `json:"package"`          // Package tier (Elite, Premier, etc.)
	Coordinator       string        `json:"coordinator"`      // Day-of coordinator name
	DJName            string        `json:"djName"`           // DJ Name
	DecorSetupTime    string        `json:"decorSetupTime"`   // Decor setup time
	BEONotes          string        `json:"beoNotes"`         // Staff Facing BEO Notes
	ContractSigned    bool          `json:"contractSigned"`   // Contract signed status
	Quote             *QuoteDetails `json:"quote,omitempty"`  // Level 2 & 3: CPQ Quote & QuoteLine details
}

// nameRelation captures relationship traversal (e.g. Venue__r.Name, Venue_GM__r.Name).
type nameRelation struct {
	Name string `json:"Name"`
}

// planningDetailsRelation captures Primary_Planning_Details__r fields.
type planningDetailsRelation struct {
	SpecialtyDrinks string `json:"Specialty_Drinks__c"`
	Chargers        string `json:"Chargers__c"`
}

// wedgewoodRecord represents custom Wedgewood schema fields.
type wedgewoodRecord struct {
	Id                        string                   `json:"Id"`
	Name                      string                   `json:"Name"`
	StageName                 string                   `json:"StageName"`
	EventStartDateOnly        string                   `json:"Event_Start_Date_Only__c"`
	DayOfWeek                 string                   `json:"Day_of_Week__c"`
	StartDateTime             string                   `json:"Start_Date_Time_Formula__c"`
	EndDateTime               string                   `json:"End_Date_Time_Formula__c"`
	EventTypeDetail           string                   `json:"Event_Type_Detail__c"`
	GuestCountFormula         interface{}              `json:"Guest_Count_Formula__c"`
	VenueName                 string                   `json:"Venue_Name__c"`
	VenueId                   string                   `json:"Venue__c"`
	VenueR                    *nameRelation            `json:"Venue__r"`                         // Venue__c lookup → Name
	VenueGMR                  *nameRelation            `json:"Venue_GM__r"`                      // Venue_GM__c lookup → Name
	CeremonyLocationR         *nameRelation            `json:"Wedgewood_Ceremony_Location_c__r"` // Area__c lookup → Name
	PhotoboothSetupTime       interface{}              `json:"Photobooth_Setup_Time__c"`
	IncludesPhotoBooth        float64                  `json:"Includes_Photo_Booth__c"`
	CocktailHourStartDateTime interface{}              `json:"Cocktail_Hour_Start_Date_Time__c"`
	BarCocktailHourLocation   string                   `json:"Bar_Cocktail_Hour_Location__c"`
	BarCocktailLocationR      *nameRelation            `json:"Bar_Cocktail_Hour_Location__r"` // Area__c lookup → Name
	OtherNotesAboutDatesTxt   string                   `json:"Other_Notes_About_Dates_Txt__c"`
	PackageFormula            string                   `json:"Package_Formula__c"`
	CoordinatorName           string                   `json:"Wedgewood_Coordinator_Name__c"`
	DJName                    string                   `json:"DJ_Name__c"`
	DecorSetupTime            string                   `json:"Decor_Setup_Time__c"`
	BEONotes                  string                   `json:"Staff_Facing_BEO_Notes__c"`
	ContractSigned            bool                     `json:"Contract_Signed__c"`
	PrimaryPlanningDetails    *planningDetailsRelation `json:"Primary_Planning_Details__r"`
	PrimaryQuoteId            string                   `json:"SBQQ__PrimaryQuote__c"`
}

// standardRecord handles standard Salesforce orgs without custom Wedgewood fields.
type standardRecord struct {
	Id          string   `json:"Id"`
	Name        string   `json:"Name"`
	CloseDate   string   `json:"CloseDate"`
	StageName   string   `json:"StageName"`
	Amount      *float64 `json:"Amount"`
	Description string   `json:"Description"`
	Type        string   `json:"Type"`
}

const wedgewoodSOQL = `
SELECT
  Id,
  Name,
  StageName,
  Event_Start_Date_Only__c,
  Day_of_Week__c,
  Start_Date_Time_Formula__c,
  End_Date_Time_Formula__c,
  Event_Type_Detail__c,
  Guest_Count_Formula__c,
  Venue__c,
  Venue_Name__c,
  Venue__r.Name,
  Venue_GM__r.Name,
  Wedgewood_Ceremony_Location_c__c,
  Wedgewood_Ceremony_Location_c__r.Name,
  Photobooth_Setup_Time__c,
  Includes_Photo_Booth__c,
  Cocktail_Hour_Start_Date_Time__c,
  Bar_Cocktail_Hour_Location__c,
  Bar_Cocktail_Hour_Location__r.Name,
  Other_Notes_About_Dates_Txt__c,
  Package_Formula__c,
  Wedgewood_Coordinator_Name__c,
  DJ_Name__c,
  Decor_Setup_Time__c,
  Staff_Facing_BEO_Notes__c,
  Contract_Signed__c,
  Primary_Planning_Details__r.Specialty_Drinks__c,
  Primary_Planning_Details__r.Chargers__c,
  SBQQ__PrimaryQuote__c
FROM Opportunity
WHERE Event_Start_Date_Only__c >= %s
  AND Event_Start_Date_Only__c <= %s
  AND StageName NOT IN ('Closed Lost', 'Cancelled')
ORDER BY Event_Start_Date_Only__c ASC
`

const standardSOQL = `
SELECT Id, Name, CloseDate, StageName, Amount, Description, Type
FROM Opportunity
WHERE CloseDate >= %s AND CloseDate <= %s
ORDER BY CloseDate ASC
`

const fallbackSOQL = `
SELECT Id, Name, CloseDate, StageName, Amount, Description, Type
FROM Opportunity
ORDER BY CloseDate DESC
LIMIT 100
`

// GetWeeklyEvents queries events for [weekStart, weekEnd].
func GetWeeklyEvents(c *Client, weekStart, weekEnd time.Time) ([]Event, error) {
	startDateStr := weekStart.Format("2006-01-02")
	endDateStr := weekEnd.Format("2006-01-02")

	// 1. Try custom Wedgewood SOQL
	soql := fmt.Sprintf(wedgewoodSOQL, startDateStr, endDateStr)
	records, err := c.Query(soql)
	if err == nil {
		events := make([]Event, 0, len(records))
		for _, raw := range records {
			var opp wedgewoodRecord
			if err := json.Unmarshal(raw, &opp); err == nil {
				// Filter out Lost or Cancelled in fast Go memory
				if opp.StageName == "Closed Lost" || opp.StageName == "Cancelled" {
					continue
				}
				events = append(events, toWedgewoodEvent(opp))
			}
		}

		// Fast in-memory sort by date then time
		sortEvents(events)
		enrichEventsWithCPQQuotes(c, events)
		return events, nil
	}

	// If error is not a missing column, return it
	if !strings.Contains(err.Error(), "INVALID_FIELD") && !strings.Contains(err.Error(), "No such column") {
		return nil, err
	}

	log.Println("Custom Wedgewood fields not found in this org — falling back to standard Opportunity fields")

	// 2. Fall back to standard Opportunity query
	stdSoql := fmt.Sprintf(standardSOQL, startDateStr, endDateStr)
	stdRecords, err := c.Query(stdSoql)
	if err != nil || len(stdRecords) == 0 {
		stdRecords, err = c.Query(fallbackSOQL)
		if err != nil {
			return nil, err
		}
	}

	events := make([]Event, 0, len(stdRecords))
	for _, raw := range stdRecords {
		var std standardRecord
		if err := json.Unmarshal(raw, &std); err == nil {
			events = append(events, toStandardEvent(std))
		}
	}
	sortEvents(events)
	return events, nil
}

func cleanTime(isoStr string) string {
	if isoStr == "" {
		return ""
	}
	// Try parsing ISO formats commonly returned by Salesforce formulas
	layouts := []string{
		"2006-01-02T15:04:05.000-0700",
		"2006-01-02T15:04:05.000+0000",
		"2006-01-02T15:04:05Z",
		time.RFC3339,
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, isoStr); err == nil {
			return t.Format("3:04 PM")
		}
	}
	return isoStr
}

func cleanBarLocation(loc string) string {
	// If it's a raw Salesforce ID like a3APq000001yYHZMA2 (15-18 chars without spaces), hide the ID
	if len(loc) >= 15 && !strings.Contains(loc, " ") {
		return ""
	}
	return loc
}

func sortEvents(events []Event) {
	sort.Slice(events, func(i, j int) bool {
		if events[i].EventDate != events[j].EventDate {
			return events[i].EventDate < events[j].EventDate
		}
		return events[i].StartTime < events[j].StartTime
	})
}

func toWedgewoodEvent(o wedgewoodRecord) Event {
	guestCount := 0
	if o.GuestCountFormula != nil {
		if f, ok := o.GuestCountFormula.(float64); ok {
			guestCount = int(f)
		}
	}

	venueProperty := ""
	if o.VenueR != nil && o.VenueR.Name != "" {
		venueProperty = o.VenueR.Name
	}

	ceremonyLocation := ""
	if o.CeremonyLocationR != nil && o.CeremonyLocationR.Name != "" {
		ceremonyLocation = o.CeremonyLocationR.Name
	}

	barLocation := ""
	if o.BarCocktailLocationR != nil && o.BarCocktailLocationR.Name != "" {
		barLocation = o.BarCocktailLocationR.Name
	} else {
		barLocation = cleanBarLocation(o.BarCocktailHourLocation)
	}

	venueGM := ""
	if o.VenueGMR != nil && o.VenueGMR.Name != "" {
		venueGM = o.VenueGMR.Name
	}

	sigDrinks := ""
	chargers := ""
	if o.PrimaryPlanningDetails != nil {
		sigDrinks = o.PrimaryPlanningDetails.SpecialtyDrinks
		chargers = o.PrimaryPlanningDetails.Chargers
	}

	return Event{
		ID:                o.Id,
		Name:              o.Name,
		StageName:         o.StageName,
		EventDate:         o.EventStartDateOnly,
		DayOfWeek:         o.DayOfWeek,
		StartTime:         cleanTime(o.StartDateTime),
		EndTime:           cleanTime(o.EndDateTime),
		EventType:         o.EventTypeDetail,
		GuestCount:        guestCount,
		Venue:             o.VenueName,
		VenueID:           o.VenueId,
		VenueProperty:     venueProperty,
		VenueGM:           venueGM,
		CeremonyLocation:  ceremonyLocation,
		PhotoBooth:        o.PhotoboothSetupTime != nil || o.IncludesPhotoBooth >= 1,
		PhotoboothStaffed: false, // Calculated during CPQ enrichment
		HasBar:            o.CocktailHourStartDateTime != nil,
		BarLocation:       barLocation,
		SignatureDrinks:   sigDrinks,
		ChargerPlates:     chargers,
		Notes:             o.OtherNotesAboutDatesTxt,
		Package:           o.PackageFormula,
		Coordinator:       o.CoordinatorName,
		DJName:            o.DJName,
		DecorSetupTime:    o.DecorSetupTime,
		BEONotes:          o.BEONotes,
		ContractSigned:    o.ContractSigned,
	}
}

func toStandardEvent(s standardRecord) Event {
	// dayOfWeek can be derived from the real CloseDate — this is not fake data
	dayOfWeek := ""
	if t, err := time.Parse("2006-01-02", s.CloseDate); err == nil {
		dayOfWeek = t.Weekday().String()
	}

	// Use real Type from Salesforce; empty string means FE shows "Not set in Salesforce"
	eventType := s.Type

	// Use real Description from Salesforce; empty string means FE shows "Not set in Salesforce"
	notes := s.Description

	return Event{
		ID:          s.Id,
		Name:        s.Name,
		StageName:   s.StageName,
		EventDate:   s.CloseDate,
		DayOfWeek:   dayOfWeek,
		StartTime:   "", // not available on standard Opportunity — FE shows "Not set in Salesforce"
		EndTime:     "", // not available on standard Opportunity — FE shows "Not set in Salesforce"
		EventType:   eventType,
		GuestCount:  0,  // not available on standard Opportunity — FE shows "Not set in Salesforce"
		Venue:       "", // not available on standard Opportunity — FE shows "Not set in Salesforce"
		PhotoBooth:  false,
		HasBar:      false,
		BarLocation: "",
		Notes:       notes,
	}
}

type quoteRecord struct {
	Id              string   `json:"Id"`
	Name            string   `json:"Name"`
	OpportunityId   string   `json:"SBQQ__Opportunity2__c"`
	IsPrimary       bool     `json:"SBQQ__Primary__c"`
	MealServiceType *string  `json:"MealServiceType__c"`
	BarServiceType  *string  `json:"Bar_Service_Type__c"`
	HostedBarHours  *float64 `json:"of_Hosted_Bar_Hours__c"`
}

type productRelation struct {
	MealServiceType   *string `json:"MealServiceType__c"`
	ConfigurationType *string `json:"SBQQ__ConfigurationType__c"`
	ServiceOfferings  *string `json:"Service_Offerings__c"`
}

type quoteLineRecord struct {
	Id                     string           `json:"Id"`
	QuoteId                string           `json:"SBQQ__Quote__c"`
	ProductName            string           `json:"SBQQ__ProductName__c"`
	Quantity               *float64         `json:"SBQQ__Quantity__c"`
	Number                 *float64         `json:"SBQQ__Number__c"`
	POSCode                *string          `json:"POS_Code__c"`
	NotIncludedInPrints    bool             `json:"NotIncludedInPrints__c"`
	ServiceOfferings       *string          `json:"Service_Offerings__c"`
	VendorAccount          *string          `json:"Vendor_Account__c"`
	ProductMealServiceType *string          `json:"ProductMealServiceType__c"`
	HouseWineSelection     *string          `json:"HouseWineSelection__c"`
	HouseLinenColor        *string          `json:"HouseLinenColor__c"`
	HouseNapkinColor       *string          `json:"HouseNapkinColor__c"`
	ProductR               *productRelation `json:"SBQQ__Product__r"`
}

func containsAny(s string, keywords ...string) bool {
	for _, kw := range keywords {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

func appendUnique(slice []string, val string) []string {
	for _, item := range slice {
		if item == val {
			return slice
		}
	}
	return append(slice, val)
}

func enrichEventsWithCPQQuotes(c *Client, events []Event) {
	if len(events) == 0 {
		return
	}

	// 1. Collect unique Opportunity IDs & map VenueIDs
	oppIDs := make([]string, 0, len(events))
	oppIndexMap := make(map[string][]int)
	oppVenueMap := make(map[string]string)
	for idx, e := range events {
		if e.ID != "" {
			if _, exists := oppIndexMap[e.ID]; !exists {
				oppIDs = append(oppIDs, e.ID)
			}
			oppIndexMap[e.ID] = append(oppIndexMap[e.ID], idx)
			if e.VenueID != "" {
				oppVenueMap[e.ID] = e.VenueID
			}
		}
	}
	if len(oppIDs) == 0 {
		return
	}

	quotedOppIDs := "'" + strings.Join(oppIDs, "','") + "'"
	quoteSOQL := fmt.Sprintf(`
SELECT 
  Id, 
  Name, 
  SBQQ__Opportunity2__c, 
  SBQQ__Primary__c, 
  MealServiceType__c, 
  Bar_Service_Type__c, 
  of_Hosted_Bar_Hours__c
FROM SBQQ__Quote__c 
WHERE SBQQ__Opportunity2__c IN (%s)
  AND SBQQ__Primary__c = TRUE
`, quotedOppIDs)

	qRecords, err := c.Query(quoteSOQL)
	if err != nil {
		log.Printf("CPQ Quote query notice: %v", err)
		return
	}

	oppToQuote := make(map[string]*QuoteDetails)
	quoteIdMap := make(map[string]*QuoteDetails)
	quoteToOppMap := make(map[string]string)
	var quoteIDs []string

	for _, raw := range qRecords {
		var q quoteRecord
		if err := json.Unmarshal(raw, &q); err != nil {
			continue
		}
		// If we already selected a quote for this opp (primary encountered first due to ORDER BY), skip secondary
		if _, exists := oppToQuote[q.OpportunityId]; exists {
			continue
		}

		mealService := ""
		if q.MealServiceType != nil {
			mealService = *q.MealServiceType
		}
		barPackage := ""
		if q.BarServiceType != nil {
			barPackage = *q.BarServiceType
		}
		var hostedHours float64
		if q.HostedBarHours != nil {
			hostedHours = *q.HostedBarHours
		}

		qd := &QuoteDetails{
			QuoteID:         q.Id,
			QuoteNumber:     q.Name,
			IsPrimary:       q.IsPrimary,
			MealService:     mealService,
			BarPackage:      barPackage,
			HostedBarHours:  hostedHours,
			LateNightSnacks: make([]string, 0),
			BarItems:        make([]string, 0),
			Appetizers:      make([]string, 0),
			Entrees:         make([]string, 0),
			SidesSalads:     make([]string, 0),
			Linens:          make([]string, 0),
			DecorRentals:    make([]string, 0),
		}

		oppToQuote[q.OpportunityId] = qd
		quoteIdMap[q.Id] = qd
		quoteToOppMap[q.Id] = q.OpportunityId
		quoteIDs = append(quoteIDs, q.Id)
	}

	if len(quoteIDs) == 0 {
		return
	}

	// 2. Query Quote Lines using exact fields from team spec
	quotedQuoteIDs := "'" + strings.Join(quoteIDs, "','") + "'"
	qlSOQL := fmt.Sprintf(`
SELECT 
  Id, 
  SBQQ__Quote__c, 
  SBQQ__ProductName__c, 
  SBQQ__Quantity__c,
  SBQQ__Number__c,
  POS_Code__c,
  NotIncludedInPrints__c,
  Service_Offerings__c,
  Vendor_Account__c,
  ProductMealServiceType__c,
  HouseWineSelection__c,
  HouseLinenColor__c,
  HouseNapkinColor__c,
  SBQQ__Product__r.MealServiceType__c,
  SBQQ__Product__r.SBQQ__ConfigurationType__c
FROM SBQQ__QuoteLine__c 
WHERE SBQQ__Quote__c IN (%s)
`, quotedQuoteIDs)

	quoteStaffedPhotobooth := make(map[string]bool)

	qlRecords, err := c.Query(qlSOQL)
	if err != nil {
		log.Printf("CPQ QuoteLine query notice: %v", err)
	} else {
		for _, raw := range qlRecords {
			var line quoteLineRecord
			if err := json.Unmarshal(raw, &line); err != nil {
				continue
			}
			qd, ok := quoteIdMap[line.QuoteId]
			if !ok {
				continue
			}

			prodName := strings.TrimSpace(line.ProductName)
			if prodName == "" {
				continue
			}
			pLower := strings.ToLower(prodName)

			posCode := ""
			if line.POSCode != nil {
				posCode = *line.POSCode
			}
			serviceOff := ""
			if line.ServiceOfferings != nil {
				serviceOff = *line.ServiceOfferings
			}

			// Spec #1: Meal Package (POS_Code__c LIKE '%Banquet Food%' AND SBQQ__Product__r.MealServiceType__c != null)
			hasProductMealType := line.ProductR != nil && line.ProductR.MealServiceType != nil && *line.ProductR.MealServiceType != ""
			if strings.Contains(posCode, "Banquet Food") && hasProductMealType {
				qd.MealPackage = prodName
				if line.Quantity != nil {
					qd.MealPackageQty = *line.Quantity
				}
			}

			// Spec #2: Hosted Bar Type (NotIncludedInPrints__c = FALSE AND Service_Offerings__c LIKE 'Bar%' AND POS_Code__c LIKE '%Hosted Bar%')
			isBarOffering := strings.HasPrefix(serviceOff, "Bar") || (line.ProductR != nil && line.ProductR.ServiceOfferings != nil && strings.HasPrefix(*line.ProductR.ServiceOfferings, "Bar"))
			if !line.NotIncludedInPrints && isBarOffering && strings.Contains(posCode, "Hosted Bar") {
				if qd.HostedBarType == "" {
					qd.HostedBarType = prodName
				}
				if qd.BarPackage == "" {
					qd.BarPackage = prodName
				}
			}

			// Spec #3: No Host Bar (NotIncludedInPrints__c = FALSE AND Service_Offerings__c LIKE 'Bar%' AND POS_Code__c LIKE '%No Host Bar%')
			if !line.NotIncludedInPrints && isBarOffering && strings.Contains(posCode, "No Host Bar") {
				qd.NoHostBar = prodName
			}

			// Spec #4: Champagne (NotIncludedInPrints__c = FALSE AND Service_Offerings__c LIKE 'Bar%' AND POS_Code__c LIKE '%Champagne%')
			if !line.NotIncludedInPrints && isBarOffering && strings.Contains(posCode, "Champagne") {
				qd.Champagne = prodName
				if line.Quantity != nil {
					qd.ChampagneQty = *line.Quantity
				}
				qd.ChampagneFlutesNeeded = true
				qd.BarItems = appendUnique(qd.BarItems, prodName)
			}

			// Spec #5: Wine (NotIncludedInPrints__c = FALSE AND Service_Offerings__c LIKE 'Bar%' AND POS_Code__c LIKE '%Hosted Wine%')
			if !line.NotIncludedInPrints && isBarOffering && strings.Contains(posCode, "Hosted Wine") {
				qd.Wine = prodName
				if line.Quantity != nil {
					qd.WineQty = *line.Quantity
				}
				qd.WineGlassesNeeded = true
				qd.BarItems = appendUnique(qd.BarItems, prodName)
			}

			// Spec #6: Late Night Snacks / Munchies ((POS_Code__c LIKE '%Banquet Food%' OR POS_Code__c LIKE '%Add-on Specialty%') AND Service_Offerings__c LIKE '%Munchies%' AND SBQQ__Product__r.SBQQ__ConfigurationType__c = null)
			isSnackPOS := strings.Contains(posCode, "Banquet Food") || strings.Contains(posCode, "Add-on Specialty")
			isMunchies := strings.Contains(serviceOff, "Munchies")
			isConfigNull := line.ProductR == nil || line.ProductR.ConfigurationType == nil || *line.ProductR.ConfigurationType == ""
			if isSnackPOS && isMunchies && isConfigNull {
				qd.LateNightSnacks = appendUnique(qd.LateNightSnacks, prodName)
			}

			// Spec #7: Love Letters (SBQQ__ProductName__c LIKE '%Love Letter%' AND (NOT SBQQ__ProductName__c LIKE 'REMOVAL%'))
			if strings.Contains(pLower, "love letter") && !strings.HasPrefix(prodName, "REMOVAL") {
				qd.LoveLetters = prodName
				if line.Quantity != nil {
					qd.LoveLettersQty = *line.Quantity
				}
			}

			// Spec #8: Photobooth Staffing (Venue__c IN (SELECT Vendor_Account__c WHERE ProductName = 'Photobooth'))
			if prodName == "Photobooth" && line.VendorAccount != nil {
				oppId := quoteToOppMap[line.QuoteId]
				venueId := oppVenueMap[oppId]
				if venueId != "" && *line.VendorAccount == venueId {
					quoteStaffedPhotobooth[line.QuoteId] = true
				}
			}

			// Skip internal administrative, fee, or pricing items
			if containsAny(pLower, "fee", "credit", "venue price", "ceremony site", "rehearsal & ceremony coordination", "ceremony officiant", "under 21 discount") {
				continue
			}

			// If meal service style wasn't on the quote header, inspect lines
			if qd.MealService == "" {
				if line.ProductMealServiceType != nil && *line.ProductMealServiceType != "" {
					qd.MealService = *line.ProductMealServiceType
				} else if strings.Contains(pLower, "buffet") {
					qd.MealService = "Buffet Service"
				} else if strings.Contains(pLower, "plated") {
					qd.MealService = "Plated Service"
				}
			}

			// Secondary bar items
			if containsAny(pLower, "open bar", "well liquor", "top-shelf", "premium liquor") {
				if qd.BarPackage == "" {
					qd.BarPackage = prodName
				}
			} else if containsAny(pLower, "champagne", "toast", "wine with meal", "beverage station") {
				qd.BarItems = appendUnique(qd.BarItems, prodName)
			} else if containsAny(pLower, "appetizer", "wing", "crab", "skewer", "dumpling", "croquette", "bites", "waffle", "bruschetta", "crostini", "egg roll", "canapes", "hors d", "cheese", "wafers") {
				qd.Appetizers = appendUnique(qd.Appetizers, prodName)
			} else if containsAny(pLower, "chicken", "beef", "steak", "pork", "ham", "salmon", "fish", "prime rib", "entree", "roast", "tortellini", "ravioli", "pasta", "duet plate", "carving station") {
				qd.Entrees = appendUnique(qd.Entrees, prodName)
			} else if containsAny(pLower, "salad", "vegetable", "potato", "bread", "butter", "rice") {
				qd.SidesSalads = appendUnique(qd.SidesSalads, prodName)
			} else if containsAny(pLower, "cake", "dessert", "cookie", "donut", "cupcake") {
				if qd.Dessert == "" {
					qd.Dessert = prodName
				}
			} else if strings.Contains(pLower, "napkin") {
				if qd.Napkins == "" {
					qd.Napkins = prodName
				}
			} else if strings.Contains(pLower, "linen") {
				qd.Linens = appendUnique(qd.Linens, prodName)
			} else if strings.Contains(pLower, "chair") {
				if qd.Chairs == "" {
					qd.Chairs = prodName
				}
			} else if containsAny(pLower, "centerpiece", "charger", "light", "floral", "photobooth", "dj & mc", "game", "lawn") {
				qd.DecorRentals = appendUnique(qd.DecorRentals, prodName)
			}

			// Add attribute details if present
			if line.HouseWineSelection != nil && *line.HouseWineSelection != "" {
				qd.BarItems = appendUnique(qd.BarItems, "Wine: "+*line.HouseWineSelection)
			}
			if line.HouseLinenColor != nil && *line.HouseLinenColor != "" {
				qd.Linens = appendUnique(qd.Linens, "Linen Color: "+*line.HouseLinenColor)
			}
			if line.HouseNapkinColor != nil && *line.HouseNapkinColor != "" {
				if qd.Napkins == "" {
					qd.Napkins = "Napkin Color: " + *line.HouseNapkinColor
				}
			}
		}
	}

	// 3. Attach QuoteDetails and calculate Photobooth staffing
	for oppId, qd := range oppToQuote {
		for _, idx := range oppIndexMap[oppId] {
			events[idx].Quote = qd
			if events[idx].PhotoBooth && quoteStaffedPhotobooth[qd.QuoteID] {
				events[idx].PhotoboothStaffed = true
			}
		}
	}
}

