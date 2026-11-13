// bootstrap-admin is an operator-only, one-time WorkOS-assisted bootstrap.
package main

import (
	"context"
	"log"
	"os"
	"time"

	workos "github.com/workos/workos-go/v7"
	"wedding-project/internal/admin"
	"wedding-project/internal/database"
)

func main() {
	apiKey := os.Getenv("WORKOS_API_KEY")
	userID := os.Getenv("BOOTSTRAP_WORKOS_USER_ID")
	organizationID := os.Getenv("BOOTSTRAP_WORKOS_ORGANIZATION_ID")
	roleSlug := os.Getenv("BOOTSTRAP_WORKOS_ROLE_SLUG")
	if apiKey == "" || userID == "" || organizationID == "" || roleSlug == "" {
		log.Fatal("missing WorkOS bootstrap configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := database.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal("database unavailable")
	}
	defer db.Close()
	verifier := admin.WorkOSVerifier{Client: workos.NewClient(apiKey)}
	if err := admin.Bootstrap(ctx, db, verifier, userID, organizationID, roleSlug); err != nil {
		log.Fatalf("bootstrap failed: %v", err)
	}
	log.Print("initial administrator bootstrapped")
}
