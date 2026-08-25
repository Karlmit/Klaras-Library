package store

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/Karlmit/Klaras-Library/migrations"
)

// Migrate brings the database up to the latest embedded schema version.
//
// goose needs a database/sql handle, so this opens one alongside the pgx pool
// for the duration of the migration and closes it again.
func Migrate(ctx context.Context, dsn string, log *slog.Logger) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open migration connection: %w", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(gooseLogger{log})
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set dialect: %w", err)
	}

	before, _ := goose.GetDBVersion(db)
	if err := goose.UpContext(ctx, db, "."); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	after, _ := goose.GetDBVersion(db)

	if before == after {
		log.Info("schema up to date", "version", after)
	} else {
		log.Info("schema migrated", "from", before, "to", after)
	}
	return nil
}

// MigrateDown rolls back a single migration. Used by `klaras migrate down`.
func MigrateDown(ctx context.Context, dsn string, log *slog.Logger) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(gooseLogger{log})
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.DownContext(ctx, db, ".")
}

// SchemaVersion reports the currently applied migration version.
func SchemaVersion(ctx context.Context, dsn string) (int64, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	if err := goose.SetDialect("postgres"); err != nil {
		return 0, err
	}
	return goose.GetDBVersion(db)
}

// gooseLogger adapts slog to goose's logger interface.
type gooseLogger struct{ log *slog.Logger }

func (g gooseLogger) Printf(format string, v ...any) {
	g.log.Debug(fmt.Sprintf(format, v...))
}

func (g gooseLogger) Fatalf(format string, v ...any) {
	g.log.Error(fmt.Sprintf(format, v...))
}

// keep the pgx stdlib driver registration referenced
var _ = stdlib.GetDefaultDriver
