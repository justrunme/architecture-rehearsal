package persist

import (
	_ "github.com/jackc/pgx/v5/stdlib" // registers database/sql driver "pgx"
)

func registerPGX() error {
	// Blank import above registers the driver as "pgx".
	return nil
}
