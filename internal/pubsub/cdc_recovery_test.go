package pubsub

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	sf "wedding-project/internal/adapters/salesforce"
)

func TestRecoveryOnlyDeletesConfirmedRecords(t *testing.T) {
	id := "006000000000001"
	for _, test := range []struct {
		name, body       string
		deleted, wantErr bool
	}{
		{"soft deleted", `{"done":true,"records":[{"Id":"006000000000001","SystemModstamp":"2026-11-27T01:00:00.000+0000","IsDeleted":true}]}`, true, false},
		{"still live", `{"done":true,"records":[{"Id":"006000000000001","SystemModstamp":"2026-11-27T01:00:00.000+0000","IsDeleted":false}]}`, false, false},
		{"missing or inaccessible", `{"done":true,"records":[]}`, false, true},
		{"missing deletion field", `{"done":true,"records":[{"Id":"006000000000001"}]}`, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/services/data/v62.0/queryAll" {
					t.Errorf("wrong endpoint %s", r.URL.Path)
				}
				fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			client, _ := sf.NewClient(sf.Config{MyDomainURL: server.URL, AccessToken: "test"})
			deleted, _, err := confirmedDeleted(context.Background(), client, id)
			if deleted != test.deleted || (err != nil) != test.wantErr {
				t.Fatalf("deleted=%v err=%v", deleted, err)
			}
		})
	}
}
