// Package store is the Linux/IoT port's persistence layer: a pure-Go SQLite
// database plus the staged-photo files that sit alongside it.
//
// Two rules shape everything here.
//
// The store APPLIES plans; it never derives them. Confirming a scan into
// inventory and cooking a recipe are decided by the pure logic in
// internal/contract, which is pinned by the Swift tests. This package replays
// the resulting actions as database operations and nothing more. A store that
// re-derives "increment or insert?" from what it happens to find in a table is
// a second implementation of the rule, and the second implementation is the one
// that drifts.
//
// And no cgo, ever. SQLite is modernc.org/sqlite precisely so CGO_ENABLED=0
// holds on every target, which is what makes a static arm64 binary droppable
// onto a Pi with no toolchain.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	// The pure-Go SQLite driver, registered as "sqlite". Blank import: the
	// package is reached only through database/sql.
	_ "modernc.org/sqlite"
)

// ErrNotFound is returned by the CRUD helpers when a row does not exist.
//
// Callers compare with errors.Is. The HTTP layer turns it into a 404; nothing
// else in this package treats a missing row as an error, because "delete
// something already gone" is a no-op, not a failure.
var ErrNotFound = errors.New("store: not found")

// timeLayout is how every timestamp column is written.
//
// RFC 3339 with an explicit UTC offset. Values are converted with .UTC() before
// formatting, so the offset is always the literal `Z` designator and a stored
// timestamp can never be a naive local time.
//
// The three fixed fractional digits are load-bearing. time.RFC3339Nano trims
// trailing zeros, so "…:00Z" and "…:00.5Z" can both come out of the same
// writer — and since '.' (0x2E) sorts before 'Z' (0x5A), the half-second value
// would sort BEFORE the whole-second one. ORDER BY dateAdded would quietly stop
// meaning "in the order they were added". Fixed width makes byte order and
// chronological order the same thing.
const timeLayout = "2006-01-02T15:04:05.000Z07:00"

// formatTime renders t for storage. Always UTC, always fixed width.
func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

// parseTime reads a stored timestamp back.
//
// Deliberately more tolerant than formatTime is strict: time.RFC3339 accepts
// any offset and any fractional precision, so a row written by an importer, by
// a future migration, or by hand still loads. Writing is narrow; reading is
// wide.
func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("store: bad timestamp %q: %w", s, err)
	}
	return t.UTC(), nil
}

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB is an open database plus its migration state.
type DB struct {
	sql  *sql.DB
	path string
}

// SQL exposes the underlying handle for the CRUD helpers in this package and
// for tests. Callers outside the store should use the typed methods.
func (d *DB) SQL() *sql.DB { return d.sql }

// Path is the database file's absolute path.
func (d *DB) Path() string { return d.path }

// Close closes the database.
func (d *DB) Close() error { return d.sql.Close() }

// Open opens (creating if needed) the database at path and migrates it forward
// to the newest embedded migration.
//
// Four PRAGMAs are set through the DSN rather than with a one-off statement
// after connecting, which matters: database/sql owns a POOL, and a PRAGMA is a
// per-connection setting. Running `PRAGMA foreign_keys=ON` once configures
// exactly one connection and leaves every later one unconstrained — a bug that
// shows up as referential integrity working on Tuesday and not on Wednesday.
// The DSN is applied to every connection the pool ever opens.
//
//   - journal_mode=WAL      readers do not block the writer, which is what lets
//     an SSE snapshot read while a scan is writing.
//   - synchronous=NORMAL    one fsync per checkpoint instead of per commit.
//     Under WAL this risks losing the last commits to a
//     power cut, never corruption — and on an SD card the
//     write amplification of FULL is a real wear problem.
//   - busy_timeout=5000     wait rather than fail on a locked database.
//   - foreign_keys=ON       SQLite defaults these OFF, so every FK in
//     0001_init.sql is decoration until this is set.
//
// _txlock=immediate is the fifth, and is not in the required list because it is
// not a PRAGMA — it makes database/sql's BeginTx issue BEGIN IMMEDIATE. Without
// it SQLite starts every transaction deferred and takes the write lock on the
// first write, so two transactions that both read then write deadlock on the
// upgrade, and busy_timeout cannot help: neither side can make progress by
// waiting. Taking the write lock up front turns that deadlock back into a wait.
func Open(path string) (*DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("store: resolving %q: %w", path, err)
	}
	// 0700: the database holds an inventory of what is in someone's fridge and
	// the hashes of their pairing tokens. Nothing here is other-readable.
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, fmt.Errorf("store: creating state directory: %w", err)
	}

	params := url.Values{}
	params.Set("_journal_mode", "WAL")
	params.Set("_synchronous", "NORMAL")
	params.Set("_busy_timeout", "5000")
	params.Set("_foreign_keys", "on")
	params.Set("_txlock", "immediate")
	dsn := (&url.URL{Scheme: "file", Path: abs, RawQuery: params.Encode()}).String()

	handle, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: opening %q: %w", abs, err)
	}
	// A small pool, not one connection: WAL exists so reads proceed during a
	// write, and pinning the pool to 1 would throw that away. Not unbounded
	// either — this runs on a box with a 4-5 GiB working set already spoken
	// for by the model.
	handle.SetMaxOpenConns(4)
	handle.SetMaxIdleConns(4)
	handle.SetConnMaxIdleTime(5 * time.Minute)

	db := &DB{sql: handle, path: abs}
	if err := handle.Ping(); err != nil {
		_ = handle.Close()
		return nil, fmt.Errorf("store: connecting to %q: %w", abs, err)
	}
	if err := db.Migrate(context.Background()); err != nil {
		_ = handle.Close()
		return nil, err
	}
	return db, nil
}

// WithTx runs fn inside a transaction, committing on success and rolling back
// on any error or panic.
//
// The panic path re-panics after rolling back rather than swallowing: a bug in
// fn must not be downgraded into a silent no-op, and leaving the transaction
// open would hold the write lock until the connection was reaped.
func (d *DB) WithTx(ctx context.Context, fn func(*sql.Tx) error) (err error) {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

// migration is one embedded .sql file.
type migration struct {
	version  int
	name     string
	sql      string
	checksum string
}

// loadMigrations reads and orders the embedded migrations.
//
// File names are NNNN_description.sql; the numeric prefix is the version and
// must be unique. Ordering is numeric, never lexicographic: a lexicographic
// sort puts 0010 before 0009 the moment the prefix width changes, and the
// resulting schema depends on how someone named a file.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("store: reading embedded migrations: %w", err)
	}
	out := make([]migration, 0, len(entries))
	seen := map[int]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		name := entry.Name()
		prefix, _, ok := strings.Cut(strings.TrimSuffix(name, ".sql"), "_")
		if !ok {
			return nil, fmt.Errorf("store: migration %q is not NNNN_description.sql", name)
		}
		version, err := strconv.Atoi(prefix)
		if err != nil {
			return nil, fmt.Errorf("store: migration %q has a non-numeric version: %w", name, err)
		}
		if other, dup := seen[version]; dup {
			return nil, fmt.Errorf("store: migrations %q and %q share version %d", other, name, version)
		}
		seen[version] = name

		body, err := fs.ReadFile(migrationsFS, "migrations/"+name)
		if err != nil {
			return nil, fmt.Errorf("store: reading migration %q: %w", name, err)
		}
		sum := sha256.Sum256(body)
		out = append(out, migration{
			version:  version,
			name:     name,
			sql:      string(body),
			checksum: hex.EncodeToString(sum[:]),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// Migrate applies every embedded migration that has not run yet, in version
// order, each in its own transaction alongside its ledger row.
//
// FORWARD ONLY. There is no down migration and there will not be one: on a
// device whose database is the user's only copy of their inventory, an
// automatic rollback that drops a column is a data-loss button with no undo.
// Fixing a bad migration means writing the next one.
//
// An already-applied migration whose file has since changed is a hard error,
// not a warning. The database no longer matches the source that claims to
// describe it, and every later assumption is built on that.
func (d *DB) Migrate(ctx context.Context) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	applied, err := d.appliedMigrations(ctx)
	if err != nil {
		return err
	}
	for _, m := range migrations {
		if known, ok := applied[m.version]; ok {
			if known != m.checksum {
				return fmt.Errorf(
					"store: migration %s was applied with checksum %s but the embedded file now hashes to %s; "+
						"an applied migration must never be edited — write the next one instead",
					m.name, known, m.checksum)
			}
			continue
		}
		if err := d.applyMigration(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

// appliedMigrations reads the ledger, treating an ABSENT ledger table as "no
// migrations applied" rather than as an error.
//
// That is what lets 0001_init.sql own the CREATE TABLE for schema_migrations,
// as the schema documents. The alternative — having the runner bootstrap the
// table with CREATE TABLE IF NOT EXISTS — would make 0001 fail on a fresh
// database, because by the time it ran the table it creates would exist.
func (d *DB) appliedMigrations(ctx context.Context) (map[int]string, error) {
	var count int
	err := d.sql.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`,
	).Scan(&count)
	if err != nil {
		return nil, fmt.Errorf("store: probing for schema_migrations: %w", err)
	}
	applied := map[int]string{}
	if count == 0 {
		return applied, nil
	}
	rows, err := d.sql.QueryContext(ctx, `SELECT version, checksum FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("store: reading schema_migrations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var version int
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return nil, fmt.Errorf("store: reading schema_migrations: %w", err)
		}
		applied[version] = checksum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading schema_migrations: %w", err)
	}
	return applied, nil
}

// applyMigration runs one migration and records it, both or neither.
//
// SQLite applies DDL transactionally, so a migration that fails half way leaves
// no tables behind and no ledger row claiming it succeeded.
func (d *DB) applyMigration(ctx context.Context, m migration) error {
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			return fmt.Errorf("store: applying migration %s: %w", m.name, err)
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, name, checksum, appliedAt) VALUES (?, ?, ?, ?)`,
			m.version, m.name, m.checksum, formatTime(time.Now()))
		if err != nil {
			return fmt.Errorf("store: recording migration %s: %w", m.name, err)
		}
		return nil
	})
	return err
}
