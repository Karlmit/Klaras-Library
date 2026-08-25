// Package testdb hands each test package its own Postgres database.
//
// Without this, every package that needs Postgres shares one database and
// mutates it: the store tests seed 30,000 synthetic books while the importer
// tests purge and load a fixture. `go test ./...` runs packages in parallel by
// default, so they would race and fail intermittently -- the worst kind of
// test failure, because it teaches people to re-run rather than investigate.
package testdb

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

var safeName = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)

// For returns a DSN for a database dedicated to suffix, creating it if needed.
// It skips the test when KLARAS_TEST_DATABASE_URL is unset, so a bare checkout
// with no Postgres still passes `go test ./...`.
func For(t *testing.T, baseDSN, suffix string) string {
	t.Helper()
	if baseDSN == "" {
		t.Skip("KLARAS_TEST_DATABASE_URL not set; skipping tests that need Postgres")
	}
	if !safeName.MatchString(suffix) {
		t.Fatalf("suffix %q must match %s", suffix, safeName)
	}

	u, err := url.Parse(baseDSN)
	if err != nil {
		t.Fatalf("parse KLARAS_TEST_DATABASE_URL: %v", err)
	}
	base := strings.TrimPrefix(u.Path, "/")
	if base == "" {
		t.Fatal("KLARAS_TEST_DATABASE_URL has no database name")
	}
	target := base + "_" + suffix

	ctx := context.Background()
	admin, err := pgx.Connect(ctx, baseDSN)
	if err != nil {
		t.Fatalf("connect to %s: %v", base, err)
	}
	defer admin.Close(ctx)

	var exists bool
	if err := admin.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)`, target).Scan(&exists); err != nil {
		t.Fatalf("look up %s: %v", target, err)
	}
	if !exists {
		// TEMPLATE template0 is required to pick a locale that differs from the
		// template default; inheriting sv-SE from the base database is what we
		// want, so clone the base cluster's settings via template0 + LOCALE.
		q := fmt.Sprintf(
			`CREATE DATABASE %s TEMPLATE template0 LOCALE_PROVIDER icu ICU_LOCALE 'sv-SE' ENCODING 'UTF8'`,
			pgx.Identifier{target}.Sanitize())
		if _, err := admin.Exec(ctx, q); err != nil &&
			!strings.Contains(err.Error(), "already exists") {
			t.Fatalf("create %s: %v", target, err)
		}
	}

	out := *u
	out.Path = "/" + target
	return out.String()
}
