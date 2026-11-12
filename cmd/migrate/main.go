package main

import (
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"log"
	"os"
)

func main() {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Fatal("DATABASE_URL is required")
	}
	m, err := migrate.New("file://internal/migrations", url)
	if err != nil {
		log.Fatal("migration initialization failed")
	}
	defer m.Close()
	if err = m.Up(); err != nil && err != migrate.ErrNoChange {
		log.Fatal("migration failed")
	}
	log.Print("migrations complete")
}
