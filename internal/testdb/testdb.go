// Package testdb hands each test package its own schema inside one Postgres.
//
// Go runs package test binaries in parallel, and both the store and the api
// tests truncate tables and load fixtures. Sharing a schema means one package
// deletes rows the other is counting, which shows up as a failure in whichever
// package happened to be slower. A schema per package makes them independent
// without needing a database per package or a serialized test run.
package testdb

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

// URL returns a connection string scoped to a schema of the given name,
// creating the schema if it does not exist. Tests skip when there is no
// database configured, which is what keeps them runnable on a laptop with
// nothing started.
func URL(t *testing.T, schema string) string {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect to %s: %v", base, err)
	}
	defer conn.Close(ctx)
	// The name comes from the caller rather than from input, and pgx has no
	// placeholder for an identifier, so it is quoted rather than bound.
	if _, err := conn.Exec(ctx, fmt.Sprintf(
		`CREATE SCHEMA IF NOT EXISTS %s`, pgx.Identifier{schema}.Sanitize())); err != nil {
		t.Fatalf("create schema %s: %v", schema, err)
	}

	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	// search_path travels as a connection parameter, so every statement on
	// this pool lands in the schema without any of the SQL knowing about it.
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}
