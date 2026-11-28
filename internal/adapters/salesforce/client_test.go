package salesforce

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestQueryDoesNotReturnPartialResults(t *testing.T) {
	for _, pageStatus := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(pageStatus), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/next" {
					w.WriteHeader(pageStatus)
					if pageStatus == http.StatusOK {
						fmt.Fprint(w, `{"done":true,"records":[{"Id":"second"}]}`)
					}
					return
				}
				fmt.Fprint(w, `{"done":false,"nextRecordsUrl":"/next","records":[{"Id":"first"}]}`)
			}))
			defer server.Close()
			client, _ := NewClient(Config{MyDomainURL: server.URL, AccessToken: "test"})
			rows, err := client.QueryContext(context.Background(), "SELECT Id FROM Opportunity")
			if pageStatus == http.StatusOK {
				if err != nil || len(rows) != 2 {
					t.Fatalf("rows=%d err=%v", len(rows), err)
				}
			} else if err == nil || rows != nil {
				t.Fatalf("partial response accepted: rows=%d err=%v", len(rows), err)
			}
		})
	}
}

func TestDeletionHistoryUsesOnlyCoveredEvidence(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	for _, mode := range []string{"covered", "partial", "malformed", "forbidden"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("start") != start.Format(time.RFC3339) || r.URL.Query().Get("end") != end.Format(time.RFC3339) {
					t.Error("wrong UTC window")
				}
				if mode == "forbidden" {
					w.WriteHeader(403)
					return
				}
				earliest := start
				if mode == "partial" {
					earliest = start.Add(2 * time.Hour)
				}
				latest := end.Format(time.RFC3339)
				if mode == "malformed" {
					latest = "bad"
				}
				fmt.Fprintf(w, `{"earliestDateAvailable":%q,"latestDateCovered":%q,"deletedRecords":[{"id":"006000000000001","deletedDate":%q}]}`, earliest.Format(time.RFC3339), latest, start.Add(time.Hour).Format(time.RFC3339))
			}))
			defer server.Close()
			client, _ := NewClient(Config{MyDomainURL: server.URL, AccessToken: "test"})
			result, err := client.DeletedOpportunities(context.Background(), start, end)
			if mode == "malformed" || mode == "forbidden" {
				if err == nil {
					t.Fatal("invalid response accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (len(result) == 1) != (mode == "covered") {
				t.Fatalf("uncovered deletion accepted: %v", result)
			}
		})
	}
}

func TestOrganizationLookupSharedByRESTAndPubSub(t *testing.T) {
	for _, caller := range []string{"REST", "PubSub"} {
		for _, scenario := range []string{"success", "missing organization", "invalid JSON", "unauthorized"} {
			t.Run(caller+"/"+scenario, func(t *testing.T) {
				token := "rest-token"
				if caller == "PubSub" {
					token = "stream-token"
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/services/oauth2/userinfo" || r.Header.Get("Authorization") != "Bearer "+token {
						t.Error("incorrect identity request or credentials")
					}
					switch scenario {
					case "success":
						fmt.Fprint(w, `{"organization_id":"org-test"}`)
					case "missing organization":
						fmt.Fprint(w, `{"organization_id":""}`)
					case "invalid JSON":
						fmt.Fprint(w, `{`)
					case "unauthorized":
						w.WriteHeader(http.StatusUnauthorized)
					}
				}))
				defer server.Close()
				var org string
				var err error
				if caller == "REST" {
					client, createErr := NewClient(Config{MyDomainURL: server.URL, AccessToken: token})
					if createErr != nil {
						t.Fatal(createErr)
					}
					org, err = client.OrganizationID(context.Background())
				} else {
					org, err = LookupOrganizationID(context.Background(), server.Client(), server.URL, token)
				}
				if scenario == "success" {
					if err != nil || org != "org-test" {
						t.Fatalf("org=%q err=%v", org, err)
					}
				} else if err == nil || org != "" {
					t.Fatalf("invalid identity accepted: org=%q err=%v", org, err)
				}
			})
		}
	}
}
