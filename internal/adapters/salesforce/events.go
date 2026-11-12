package salesforce

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"wedding-project/internal/models"
)

// Re-export model types so existing callers remain 100% compatible.
type QuoteDetails = models.QuoteDetails
type Event = models.Event


// nameRelation captures relationship traversal (e.g. Venue__r.Name, Venue_GM__r.Name).
type nameRelation struct {
	Name string `json:"Name"`
}

// planningDetailsRelation captures Primary_Planning_Details__r fields.
type planningDetailsRelation struct {
	SpecialtyDrinks           string `json:"Specialty_Drinks__c"`
	Chargers                  string `json:"Chargers__c"`
	CeremonyStartTime         string `json:"Ceremony_Start_Time__c"`
	CeremonyStartDateTime     string `json:"Ceremony_Start_DateTime__c"`
	MealServiceTime           string `json:"Meal_Service_Time__c"`
	MealServiceDateTime       string `json:"Meal_Service_DateTime__c"`
	CocktailHourStartTime     string `json:"Cocktail_Hour_Start_Time__c"`
	CocktailHourStartDateTime string `json:"Cocktail_Hour_Start_DateTime__c"`
	BarStartTime              string `json:"Bar_Start_Time__c"`
	BarStartDateTime          string `json:"Bar_Start_Date_Time__c"`
	BarCloseTime              string `json:"Bar_Close_Time__c"`
	BarCloseDateTime          string `json:"Bar_Close_DateTime__c"`
	BarLastCallTime           string `json:"Bar_Last_Call_Time__c"`
	BarLastCallDateTime       string `json:"Bar_Last_Call_DateTime__c"`
	AnticipatedBarActivity    string `json:"Anticipated_Bar_Activity__c"`
	OtherBarNotes             string `json:"Other_Bar_Notes__c"`
	BeverageCartDetails       string `json:"Beverage_Cart_Details__c"`
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
	Under21Formula            *float64                 `json:"Under_21_Formula__c"`
	GuestOver21Formula        *float64                 `json:"Guest_Over_21_Formula__c"`
	VenueName                 string                   `json:"Venue_Name__c"`
	VenueId                   string                   `json:"Venue__c"`
	VenueR                    *nameRelation            `json:"Venue__r"`                         // Venue__c lookup → Name
	VenueGMR                  *nameRelation            `json:"Venue_GM__r"`                      // Venue_GM__c lookup → Name
	CeremonyLocationR         *nameRelation            `json:"Wedgewood_Ceremony_Location_c__r"` // Area__c lookup → Name
	PhotoboothSetupTime       interface{}              `json:"Photobooth_Setup_Time__c"`
	IncludesPhotoBooth        *float64                 `json:"Includes_Photo_Booth__c"`
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
  Under_21_Formula__c,
  Guest_Over_21_Formula__c,
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
  Primary_Planning_Details__r.Ceremony_Start_Time__c,
  Primary_Planning_Details__r.Ceremony_Start_DateTime__c,
  Primary_Planning_Details__r.Meal_Service_Time__c,
  Primary_Planning_Details__r.Meal_Service_DateTime__c,
  Primary_Planning_Details__r.Cocktail_Hour_Start_Time__c,
  Primary_Planning_Details__r.Cocktail_Hour_Start_DateTime__c,
  Primary_Planning_Details__r.Bar_Start_Time__c,
  Primary_Planning_Details__r.Bar_Start_Date_Time__c,
  Primary_Planning_Details__r.Bar_Close_Time__c,
  Primary_Planning_Details__r.Bar_Close_DateTime__c,
  Primary_Planning_Details__r.Bar_Last_Call_Time__c,
  Primary_Planning_Details__r.Bar_Last_Call_DateTime__c,
  Primary_Planning_Details__r.Anticipated_Bar_Activity__c,
  Primary_Planning_Details__r.Other_Bar_Notes__c,
  Primary_Planning_Details__r.Beverage_Cart_Details__c,
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
	var anticipatedBarActivity, otherBarNotes, beverageCartDetails, ceremonyStartTime, ceremonyStartDateTime string
	var mealServiceTime, mealServiceDateTime, cocktailHourStartTime, cocktailHourStartDateTime string
	var barStartTime, barStartDateTime, barCloseTime, barCloseDateTime, barLastCallTime, barLastCallDateTime string
	if o.PrimaryPlanningDetails != nil {
		sigDrinks = o.PrimaryPlanningDetails.SpecialtyDrinks
		chargers = o.PrimaryPlanningDetails.Chargers
		p := o.PrimaryPlanningDetails
		anticipatedBarActivity = p.AnticipatedBarActivity
		otherBarNotes = p.OtherBarNotes
		beverageCartDetails = p.BeverageCartDetails
		ceremonyStartTime, ceremonyStartDateTime = p.CeremonyStartTime, p.CeremonyStartDateTime
		mealServiceTime, mealServiceDateTime = p.MealServiceTime, p.MealServiceDateTime
		cocktailHourStartTime, cocktailHourStartDateTime = p.CocktailHourStartTime, p.CocktailHourStartDateTime
		barStartTime, barStartDateTime = p.BarStartTime, p.BarStartDateTime
		barCloseTime, barCloseDateTime = p.BarCloseTime, p.BarCloseDateTime
		barLastCallTime, barLastCallDateTime = p.BarLastCallTime, p.BarLastCallDateTime
	}

	var photoBooth *bool
	if o.PhotoboothSetupTime != nil {
		booked := true
		photoBooth = &booked
	} else if o.IncludesPhotoBooth != nil {
		booked := *o.IncludesPhotoBooth >= 1
		photoBooth = &booked
	}

	return Event{
		ID:                        o.Id,
		Name:                      o.Name,
		StageName:                 o.StageName,
		EventDate:                 o.EventStartDateOnly,
		DayOfWeek:                 o.DayOfWeek,
		StartTime:                 cleanTime(o.StartDateTime),
		EndTime:                   cleanTime(o.EndDateTime),
		EventType:                 o.EventTypeDetail,
		GuestCount:                guestCount,
		Under21Count:              o.Under21Formula,
		Over21Count:               o.GuestOver21Formula,
		Venue:                     o.VenueName,
		VenueID:                   o.VenueId,
		VenueProperty:             venueProperty,
		VenueGM:                   venueGM,
		CeremonyLocation:          ceremonyLocation,
		PhotoBooth:                photoBooth,
		PhotoboothStaffed:         false, // Calculated during CPQ enrichment
		HasBar:                    o.CocktailHourStartDateTime != nil,
		BarLocation:               barLocation,
		AnticipatedBarActivity:    anticipatedBarActivity,
		OtherBarNotes:             otherBarNotes,
		BeverageCartDetails:       beverageCartDetails,
		CeremonyStartTime:         ceremonyStartTime,
		CeremonyStartDateTime:     ceremonyStartDateTime,
		MealServiceTime:           mealServiceTime,
		MealServiceDateTime:       mealServiceDateTime,
		CocktailHourStartTime:     cocktailHourStartTime,
		CocktailHourStartDateTime: cocktailHourStartDateTime,
		BarStartTime:              barStartTime,
		BarStartDateTime:          barStartDateTime,
		BarCloseTime:              barCloseTime,
		BarCloseDateTime:          barCloseDateTime,
		BarLastCallTime:           barLastCallTime,
		BarLastCallDateTime:       barLastCallDateTime,
		SignatureDrinks:           sigDrinks,
		ChargerPlates:             chargers,
		Notes:                     o.OtherNotesAboutDatesTxt,
		Package:                   o.PackageFormula,
		Coordinator:               o.CoordinatorName,
		DJName:                    o.DJName,
		DecorSetupTime:            o.DecorSetupTime,
		BEONotes:                  o.BEONotes,
		ContractSigned:            o.ContractSigned,
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
		qd := &QuoteDetails{
			QuoteID:         q.Id,
			QuoteNumber:     q.Name,
			IsPrimary:       q.IsPrimary,
			MealService:     mealService,
			BarPackage:      barPackage,
			HostedBarHours:  q.HostedBarHours,
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
			if events[idx].PhotoBooth != nil && *events[idx].PhotoBooth && quoteStaffedPhotobooth[qd.QuoteID] {
				events[idx].PhotoboothStaffed = true
			}
		}
	}
}

// GetEventByID fetches a single Opportunity by Salesforce ID and returns it
// as a fully-enriched Event (same pipeline as GetWeeklyEvents).
func GetEventByID(c *Client, id string) (*Event, error) {
	soql := fmt.Sprintf(`SELECT
  Id, Name, StageName,
  Event_Start_Date_Only__c, Day_of_Week__c,
  Start_Date_Time_Formula__c, End_Date_Time_Formula__c,
  Event_Type_Detail__c, Guest_Count_Formula__c,
	Under_21_Formula__c, Guest_Over_21_Formula__c,
  Venue__c, Venue_Name__c, Venue__r.Name, Venue_GM__r.Name,
  Wedgewood_Ceremony_Location_c__c, Wedgewood_Ceremony_Location_c__r.Name,
  Photobooth_Setup_Time__c, Includes_Photo_Booth__c,
  Cocktail_Hour_Start_Date_Time__c,
  Bar_Cocktail_Hour_Location__c, Bar_Cocktail_Hour_Location__r.Name,
  Other_Notes_About_Dates_Txt__c, Package_Formula__c,
  Wedgewood_Coordinator_Name__c, DJ_Name__c, Decor_Setup_Time__c,
  Staff_Facing_BEO_Notes__c, Contract_Signed__c,
  Primary_Planning_Details__r.Specialty_Drinks__c,
  Primary_Planning_Details__r.Chargers__c,
	Primary_Planning_Details__r.Ceremony_Start_Time__c,
	Primary_Planning_Details__r.Ceremony_Start_DateTime__c,
	Primary_Planning_Details__r.Meal_Service_Time__c,
	Primary_Planning_Details__r.Meal_Service_DateTime__c,
	Primary_Planning_Details__r.Cocktail_Hour_Start_Time__c,
	Primary_Planning_Details__r.Cocktail_Hour_Start_DateTime__c,
	Primary_Planning_Details__r.Bar_Start_Time__c,
	Primary_Planning_Details__r.Bar_Start_Date_Time__c,
	Primary_Planning_Details__r.Bar_Close_Time__c,
	Primary_Planning_Details__r.Bar_Close_DateTime__c,
	Primary_Planning_Details__r.Bar_Last_Call_Time__c,
	Primary_Planning_Details__r.Bar_Last_Call_DateTime__c,
	Primary_Planning_Details__r.Anticipated_Bar_Activity__c,
	Primary_Planning_Details__r.Other_Bar_Notes__c,
	Primary_Planning_Details__r.Beverage_Cart_Details__c,
  SBQQ__PrimaryQuote__c
FROM Opportunity
WHERE Id = '%s'
LIMIT 1`, id)

	records, err := c.Query(soql)
	if err != nil {
		return nil, fmt.Errorf("SOQL query for event %s: %w", id, err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("event %s not found in Salesforce", id)
	}

	var opp wedgewoodRecord
	if err := json.Unmarshal(records[0], &opp); err != nil {
		return nil, fmt.Errorf("unmarshal event %s: %w", id, err)
	}

	evt := toWedgewoodEvent(opp)

	// Enrich with CPQ quote (reuse weekly enrichment on a 1-element slice)
	events := []Event{evt}
	enrichEventsWithCPQQuotes(c, events)
	return &events[0], nil
}
