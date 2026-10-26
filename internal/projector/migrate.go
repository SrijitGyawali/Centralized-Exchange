package projector

import (
	"context"
	"embed"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/tern/v2/migrate"
)

// The SQL migrations are compiled INTO the binary with go:embed, so a
// deployed cexd always carries exactly the schema its code expects. There is
// no separate migrations folder to forget to copy.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

// versionTable is where tern records which migrations have run.
const versionTable = "schema_version"

// Migrate brings the database schema up to date. It is safe to call on every
// start: migrations that already ran are skipped. tern runs each migration in
// a transaction, so a failed one leaves nothing half-applied.
//
// Rule: never edit a migration that has been applied anywhere. Add a new,
// higher-numbered file instead.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx) // tern needs a single connection, not a pool
	if err != nil {
		return fmt.Errorf("projector: migrate: %w", err)
	}
	defer conn.Release()

	m, err := migrate.NewMigrator(ctx, conn.Conn(), versionTable)
	if err != nil {
		return fmt.Errorf("projector: migrate: %w", err)
	}
	files, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return err
	}
	if err := m.LoadMigrations(files); err != nil {
		return fmt.Errorf("projector: loading migrations: %w", err)
	}
	if err := m.Migrate(ctx); err != nil {
		return fmt.Errorf("projector: migrate: %w", err)
	}
	return nil
}
