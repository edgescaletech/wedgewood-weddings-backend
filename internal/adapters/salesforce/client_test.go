package salesforce

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
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
