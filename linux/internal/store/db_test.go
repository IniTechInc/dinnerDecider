package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// openTestDB opens a throwaway database in a per-test temp directory.
//
// A real file, not ":memory:", deliberately: WAL, busy_timeout and the
// on-disk migration ledger are the things worth testing, and an in-memory
// database has none of them.
func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "dinnerdecider.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestOpenAppliesPragmas(t *testing.T) {
	db := openTestDB(t)

	cases := []struct {
		pragma string
		want   string
	}{
		{"journal_mode", "wal"},
		// 1 == NORMAL.
		{"synchronous", "1"},
		{"busy_timeout", "5000"},
		// 1 == ON. SQLite defaults this OFF, so without the DSN every foreign
		// key in 0001_init.sql is decoration.
		{"foreign_keys", "1"},
	}
	for _, tc := range cases {
		var got string
		if err := db.SQL().QueryRow("PRAGMA " + tc.pragma).Scan(&got); err != nil {
			t.Fatalf("PRAGMA %s: %v", tc.pragma, err)
		}
		if !strings.EqualFold(got, tc.want) {
			t.Errorf("PRAGMA %s = %q, want %q", tc.pragma, got, tc.want)
		}
	}
}

// The PRAGMAs must hold on EVERY pooled connection, not just the first one.
// Setting them with a single post-connect statement configures exactly one
// connection and leaves every later one unconstrained.
func TestPragmasHoldOnEveryPooledConnection(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Hold several connections open at once so the pool is forced to dial new
	// ones rather than handing back the same warm connection.
	var conns []*sql.Conn
	for i := 0; i < 4; i++ {
		c, err := db.SQL().Conn(ctx)
		if err != nil {
			t.Fatalf("Conn %d: %v", i, err)
		}
		conns = append(conns, c)
	}
	for i, c := range conns {
		var fk, journal string
		if err := c.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatalf("conn %d foreign_keys: %v", i, err)
		}
		if err := c.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
			t.Fatalf("conn %d journal_mode: %v", i, err)
		}
		if fk != "1" {
			t.Errorf("conn %d: foreign_keys = %q, want 1", i, fk)
		}
		if !strings.EqualFold(journal, "wal") {
			t.Errorf("conn %d: journal_mode = %q, want wal", i, journal)
		}
		_ = c.Close()
	}
}

func TestMigrateCreatesEveryTable(t *testing.T) {
	db := openTestDB(t)

	want := []string{
		"auth_token",
		"inventory_item",
		"photo",
		"planned_meal",
		"runtime_state",
		"scan",
		"scan_item",
		"schema_migrations",
		"setting",
		"shopping_list_item",
		"taste_profile",
	}
	rows, err := db.SQL().Query(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("listing tables: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("tables = %v (%d), want %v (%d)", got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("table[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestMigrateRecordsLedger(t *testing.T) {
	db := openTestDB(t)

	var version int
	var name, checksum, appliedAt string
	err := db.SQL().QueryRow(
		`SELECT version, name, checksum, appliedAt FROM schema_migrations ORDER BY version`).
		Scan(&version, &name, &checksum, &appliedAt)
	if err != nil {
		t.Fatalf("reading ledger: %v", err)
	}
	if version != 1 {
		t.Errorf("version = %d, want 1", version)
	}
	if name != "0001_init.sql" {
		t.Errorf("name = %q, want 0001_init.sql", name)
	}
	if len(checksum) != 64 {
		t.Errorf("checksum = %q, want a 64-char sha256 hex digest", checksum)
	}
	if _, err := parseTime(appliedAt); err != nil {
		t.Errorf("appliedAt: %v", err)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dinnerdecider.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := db.SQL().Exec(
		`INSERT INTO runtime_state (key, value, updatedAt) VALUES ('probe', 'kept', ?)`,
		formatTime(time.Now())); err != nil {
		t.Fatalf("writing probe row: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer reopened.Close()

	// Re-running must not re-apply: a second ledger row, or a wiped probe row,
	// would both mean the migration ran twice.
	var ledgerRows int
	if err := reopened.SQL().QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&ledgerRows); err != nil {
		t.Fatalf("counting ledger: %v", err)
	}
	if ledgerRows != 1 {
		t.Errorf("schema_migrations rows = %d, want 1", ledgerRows)
	}
	var probe string
	if err := reopened.SQL().QueryRow(`SELECT value FROM runtime_state WHERE key = 'probe'`).Scan(&probe); err != nil {
		t.Fatalf("reading probe row: %v", err)
	}
	if probe != "kept" {
		t.Errorf("probe = %q, want kept", probe)
	}
}

// An applied migration whose file has since changed must be a hard error. The
// database no longer matches the source claiming to describe it.
func TestMigrateRejectsAnEditedAppliedMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dinnerdecider.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Rewrite the recorded checksum, which is indistinguishable from someone
	// editing 0001_init.sql after it had already been applied.
	if _, err := db.SQL().Exec(
		`UPDATE schema_migrations SET checksum = 'deadbeef' WHERE version = 1`); err != nil {
		t.Fatalf("tampering with ledger: %v", err)
	}
	err = db.Migrate(context.Background())
	if err == nil {
		t.Fatal("Migrate accepted an edited applied migration; want an error")
	}
	if !strings.Contains(err.Error(), "must never be edited") {
		t.Errorf("error = %v, want it to explain that applied migrations are immutable", err)
	}
	_ = db.Close()
}

func TestLoadMigrationsOrdersNumerically(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(migrations) == 0 {
		t.Fatal("no embedded migrations found; the //go:embed pattern is wrong")
	}
	if !sort.SliceIsSorted(migrations, func(i, j int) bool {
		return migrations[i].version < migrations[j].version
	}) {
		t.Error("migrations are not in ascending version order")
	}
	if migrations[0].version != 1 || migrations[0].name != "0001_init.sql" {
		t.Errorf("first migration = %d/%s, want 1/0001_init.sql", migrations[0].version, migrations[0].name)
	}
}

func TestForeignKeysAreEnforced(t *testing.T) {
	db := openTestDB(t)

	_, err := db.SQL().Exec(
		`INSERT INTO scan_item (id, scanId, position, name, categoryRaw, confidence)
		 VALUES ('a', 'no-such-scan', 0, 'Milk', 'dairy', 0.9)`)
	if err == nil {
		t.Fatal("inserted a scan_item pointing at a missing scan; foreign keys are not being enforced")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "foreign key") {
		t.Errorf("error = %v, want a foreign key violation", err)
	}
}

// Deleting a scan must not cascade photo rows away: the files on disk would
// survive with nothing left pointing at them. SET NULL leaves the rows for the
// retention sweeper to remove along with their files.
func TestDeletingAScanLeavesItsPhotoRowsBehind(t *testing.T) {
	db := openTestDB(t)
	now := formatTime(time.Now())

	if _, err := db.SQL().Exec(
		`INSERT INTO scan (id, phase, startedAt) VALUES ('scan-1', 'scanning', ?)`, now); err != nil {
		t.Fatalf("insert scan: %v", err)
	}
	if _, err := db.SQL().Exec(
		`INSERT INTO photo (id, scanId, path, byteSize, width, height, mimeType, position, createdAt)
		 VALUES ('photo-1', 'scan-1', 'photo-1.jpg', 1, 2, 3, 'image/jpeg', 0, ?)`, now); err != nil {
		t.Fatalf("insert photo: %v", err)
	}
	if _, err := db.SQL().Exec(`DELETE FROM scan WHERE id = 'scan-1'`); err != nil {
		t.Fatalf("delete scan: %v", err)
	}

	var scanID sql.NullString
	if err := db.SQL().QueryRow(`SELECT scanId FROM photo WHERE id = 'photo-1'`).Scan(&scanID); err != nil {
		t.Fatalf("photo row is gone after deleting its scan: %v", err)
	}
	if scanID.Valid {
		t.Errorf("photo.scanId = %q, want NULL", scanID.String)
	}
}

func TestWithTxCommits(t *testing.T) {
	db := openTestDB(t)
	err := db.WithTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO runtime_state (key, value, updatedAt) VALUES ('k', 'v', ?)`,
			formatTime(time.Now()))
		return err
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}
	var value string
	if err := db.SQL().QueryRow(`SELECT value FROM runtime_state WHERE key = 'k'`).Scan(&value); err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if value != "v" {
		t.Errorf("value = %q, want v", value)
	}
}

func TestWithTxRollsBackOnError(t *testing.T) {
	db := openTestDB(t)
	sentinel := errors.New("nope")
	err := db.WithTx(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO runtime_state (key, value, updatedAt) VALUES ('k', 'v', ?)`,
			formatTime(time.Now())); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("WithTx err = %v, want the sentinel unwrapped", err)
	}
	var count int
	if err := db.SQL().QueryRow(`SELECT count(*) FROM runtime_state`).Scan(&count); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if count != 0 {
		t.Errorf("runtime_state rows = %d after a rolled-back transaction, want 0", count)
	}
}

// A panic inside the callback must roll back AND keep panicking. Swallowing it
// would downgrade a bug into a silent no-op, and leaving the transaction open
// would hold the write lock until the connection was reaped.
func TestWithTxRollsBackAndRepanics(t *testing.T) {
	db := openTestDB(t)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("WithTx swallowed the panic")
			}
		}()
		_ = db.WithTx(context.Background(), func(tx *sql.Tx) error {
			_, _ = tx.Exec(`INSERT INTO runtime_state (key, value, updatedAt) VALUES ('k', 'v', ?)`,
				formatTime(time.Now()))
			panic("boom")
		})
	}()

	var count int
	if err := db.SQL().QueryRow(`SELECT count(*) FROM runtime_state`).Scan(&count); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if count != 0 {
		t.Errorf("runtime_state rows = %d after a panicking transaction, want 0", count)
	}
}

func TestFormatTimeIsUTCAndFixedWidth(t *testing.T) {
	// A non-UTC zone with a non-zero offset, so a writer that forgot .UTC()
	// would emit "+05:30" and fail here.
	kolkata := time.FixedZone("IST", 5*3600+1800)
	got := formatTime(time.Date(2026, 9, 15, 4, 5, 6, 700_000_000, kolkata))
	const want = "2026-09-14T22:35:06.700Z"
	if got != want {
		t.Errorf("formatTime = %q, want %q", got, want)
	}

	// Every timestamp is the same length, whatever the fraction.
	whole := formatTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	frac := formatTime(time.Date(2026, 1, 2, 3, 4, 5, 500_000_000, time.UTC))
	if len(whole) != len(frac) {
		t.Errorf("widths differ: %q (%d) vs %q (%d)", whole, len(whole), frac, len(frac))
	}
}

// The reason the fraction is fixed width: '.' (0x2E) sorts before 'Z' (0x5A),
// so a trimmed fraction makes ORDER BY dateAdded disagree with chronological
// order. time.RFC3339Nano would fail this test; the layout in db.go passes it.
func TestFormatTimeSortsChronologically(t *testing.T) {
	earlier := formatTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	later := formatTime(time.Date(2026, 1, 2, 3, 4, 5, 500_000_000, time.UTC))
	if !(earlier < later) {
		t.Errorf("byte order disagrees with chronological order: %q should sort before %q", earlier, later)
	}
}

func TestParseTimeIsTolerant(t *testing.T) {
	cases := []string{
		"2026-09-15T04:05:06.700Z",    // what formatTime writes
		"2026-09-15T04:05:06Z",        // no fraction at all
		"2026-09-15T04:05:06.7Z",      // trimmed fraction, e.g. RFC3339Nano
		"2026-09-15T09:35:06+05:30",   // a numeric offset from some other writer
		"2026-09-15T04:05:06.123456Z", // microseconds
	}
	for _, in := range cases {
		got, err := parseTime(in)
		if err != nil {
			t.Errorf("parseTime(%q): %v", in, err)
			continue
		}
		if got.Location() != time.UTC {
			t.Errorf("parseTime(%q) location = %v, want UTC", in, got.Location())
		}
	}
	if _, err := parseTime("not a time"); err == nil {
		t.Error("parseTime accepted garbage")
	}
}
