package store

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Files are named <version>_<description>.sql and applied in version order.
// Each runs inside its own transaction together with the row recording it, so
// a migration cannot be marked applied unless its DDL committed.
type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		name := e.Name()
		version, _, ok := strings.Cut(name, "_")
		if !ok {
			return nil, fmt.Errorf("migration %q is not <version>_<name>.sql", name)
		}
		n, err := strconv.Atoi(version)
		if err != nil {
			return nil, fmt.Errorf("migration %q has a non-numeric version: %w", name, err)
		}
		body, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: n, name: name, sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })

	for i := 1; i < len(out); i++ {
		if out[i].version == out[i-1].version {
			return nil, fmt.Errorf("duplicate migration version %d", out[i].version)
		}
	}
	return out, nil
}

// migrate brings the database up to the latest embedded version.
//
// The advisory lock is held for the whole run rather than per migration, so
// several instances booting against a fresh database serialize instead of
// racing: the first applies everything, the rest find nothing to do. Advisory
// locks are session scoped, so this has to stay on one pooled connection.
func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	const lockKey = 894273041 // arbitrary app-wide constant
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
		return err
	}
	defer func() {
		if _, err := conn.Exec(context.WithoutCancel(ctx),
			"SELECT pg_advisory_unlock($1)", lockKey); err != nil {
			slog.Error("release migration lock", "err", err)
		}
	}()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
		    version     INT PRIMARY KEY,
		    name        TEXT NOT NULL,
		    applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	rows, err := conn.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	applied := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, m.sql); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
		if _, err := tx.Exec(ctx,
			"INSERT INTO schema_migrations (version, name) VALUES ($1, $2)",
			m.version, m.name); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration %s: %w", m.name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", m.name, err)
		}
		slog.Info("applied migration", "version", m.version, "name", m.name)
	}
	return nil
}
