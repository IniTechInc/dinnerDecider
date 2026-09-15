-- 0001_init.sql — the whole persisted state of the Linux/IoT port.
--
-- Field-for-field from shared/schema/*.schema.json. Column names are the
-- schemas' own camelCase property names verbatim (categoryRaw, dateAdded,
-- isStaple, plannedDate, calendarEventId, ...) so a reader can diff a table
-- against a schema file without a translation table in their head.
--
-- Three encodings the schemas leave TODO(unresolved). Decided here, and
-- recorded in docs/PERSISTENCE.md:
--
--  1. dateAdded / plannedDate  -> TEXT, RFC 3339 with an explicit UTC offset.
--     Written as "2006-01-02T15:04:05.000Z07:00" after .UTC(), so the offset is
--     always the literal RFC 3339 `Z` designator and never a naive local
--     timestamp. The FRACTIONAL PART IS FIXED-WIDTH (three digits, always
--     present) on purpose: time.RFC3339Nano trims trailing zeros, and because
--     '.' (0x2E) sorts before 'Z' (0x5A) a variable-width fraction makes
--     ORDER BY dateAdded disagree with chronological order. Reads parse with
--     time.RFC3339, which accepts any offset and any precision, so rows written
--     by some other writer still load.
--
--  2. isStaple -> INTEGER NOT NULL DEFAULT 0. SQLite has no BOOLEAN; 0/1 with
--     the default spelled out, matching `var isStaple: Bool = false`
--     (InventoryItem.swift:59-62), which exists so a store written before the
--     field did still opens.
--
--  3. categoryRaw -> TEXT NOT NULL, NEVER an ordinal. An ordinal breaks the
--     moment the enum is reordered, and is unrecoverable from stored data —
--     the row keeps an integer nobody can map back. shared/taxonomy/categories.md
--     § "Where the raw values appear" makes the raw string the storage form; the
--     Go side persists FoodCategory's rawValue (the canonical lower-case form),
--     never the model's original mixed-case string.
--
-- Rows carry an `id TEXT PRIMARY KEY` (hand-rolled UUIDv4) where iOS leaned on
-- SwiftData's implicit identity. That is a port addition, not a contract
-- change: the HTTP API addresses rows individually (PATCH/DELETE
-- /api/inventory/{id}), and identity has to be something more stable than a
-- list position.

-- The migration ledger. version is the numeric prefix of the file name;
-- checksum is the sha256 of the file's bytes as applied, so an edited
-- already-applied migration is caught instead of silently diverging.
CREATE TABLE schema_migrations (
    version   INTEGER PRIMARY KEY,
    name      TEXT NOT NULL,
    checksum  TEXT NOT NULL,
    appliedAt TEXT NOT NULL
);

-- shared/schema/inventory-item.schema.json
--
-- The merge key (trim(lower(name)) + "|" + trim(lower(brand ?? ""))) is
-- deliberately NOT a column. It is derived, the schema says so explicitly, and
-- a stored copy is one rename away from disagreeing with the function every
-- plan is expressed in.
CREATE TABLE inventory_item (
    id            TEXT    PRIMARY KEY,
    name          TEXT    NOT NULL,
    brand         TEXT,
    categoryRaw   TEXT    NOT NULL,
    quantity      INTEGER NOT NULL,
    dateAdded     TEXT    NOT NULL,
    sourcePhotoID TEXT,
    isStaple      INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_inventory_item_dateAdded ON inventory_item (dateAdded);
CREATE INDEX idx_inventory_item_name      ON inventory_item (name);

-- shared/schema/shopping-list-item.schema.json
CREATE TABLE shopping_list_item (
    id        TEXT    PRIMARY KEY,
    name      TEXT    NOT NULL,
    isChecked INTEGER NOT NULL DEFAULT 0,
    dateAdded TEXT    NOT NULL,
    isManual  INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_shopping_list_item_dateAdded ON shopping_list_item (dateAdded);

-- shared/schema/recipe.schema.json § $defs/PlannedMeal
--
-- ingredientNames and steps are ordered string arrays with no independent
-- identity, so they are stored as JSON arrays rather than as child tables:
-- order is part of the value (steps are numbered in the UI), and nothing ever
-- queries one ingredient across meals.
--
-- calendarEventId is nullable and UNUSED on Linux — there is no Calendar to
-- sync to. The column is kept so the field set stays compatible with iOS and a
-- future import does not have to invent it.
CREATE TABLE planned_meal (
    id              TEXT    PRIMARY KEY,
    recipeName      TEXT    NOT NULL,
    ingredientNames TEXT    NOT NULL,
    steps           TEXT    NOT NULL,
    timeMinutes     INTEGER NOT NULL,
    plannedDate     TEXT    NOT NULL,
    calendarEventId TEXT
);

CREATE INDEX idx_planned_meal_plannedDate ON planned_meal (plannedDate);

-- shared/schema/taste-profile.schema.json
--
-- ONE ROW, ONE BLOB. The schema is the real on-disk shape on iOS (Swift encodes
-- the struct and stores it in UserDefaults under "tasteProfile"), so the blob
-- travels between platforms unmodified. Shredding it into columns would lose
-- that and would make it far too easy to write a PARTIAL profile, which is the
-- documented trap: Swift's synthesized Decodable init does not fall back to a
-- property default for a missing key on a non-optional property, so a partial
-- blob fails to decode ENTIRELY and TasteProfile.load() returns nil — silently
-- losing the profile, allergies included.
--
-- The CHECK pins it to a single row so "which profile?" is never a question.
CREATE TABLE taste_profile (
    id        INTEGER PRIMARY KEY CHECK (id = 1),
    profile   TEXT    NOT NULL,
    updatedAt TEXT    NOT NULL
);

-- The UserDefaults equivalent: a flat key/value table.
--
-- TEN keys, and they are the EXACT iOS strings (Preferences.swift `enum
-- PrefKey`, nine of them, plus ModelFileLocator.selectedModelKey) so a future
-- import from an iOS backup is mechanical rather than a mapping exercise.
--
-- Note `defaultHouseholdSize = 2` is a default VALUE, not an eleventh key.
--
-- And note this count has nothing to do with the taste profile's 13 required
-- JSON keys. Ten settings, thirteen taste keys, unrelated things; they are only
-- ever confused because both are "the settings".
CREATE TABLE setting (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

INSERT INTO setting (key, value) VALUES
    -- DietPreference.none.rawValue. `none` is the sentinel that OMITS the
    -- "Diet:" line from the recipe prompt entirely (AppModel.swift:498).
    ('dietPreference',             'none'),
    ('allergies',                  ''),
    ('cuisineLikes',               ''),
    -- PrefKey.defaultHouseholdSize.
    ('householdSize',              '2'),
    ('hasSeenModelSetup',          'false'),
    ('hasSeenOnboarding',          'false'),
    -- InventorySort.category.rawValue, the @AppStorage default in InventoryView.
    ('inventorySortOrder',         'category'),
    -- Hidden debug switch that forces a malformed recipe response so the error
    -- UI can be demonstrated. Off in normal use.
    ('debugSimulateRecipeFailure', 'false'),
    ('hasCompletedTasteProfile',   'false'),
    -- ModelFileLocator.selectedModelKey. Empty means "no user pin": the
    -- locator's own preference order decides.
    ('selectedModelFileName',      '');

-- One scan run.
--
-- `phase` stores the ScanPhase RAW STRING (idle|scanning|finished|empty|failed|
-- cancelled) — same rule as categoryRaw, and for the same reason: an ordinal
-- for a six-value enum that grew a value in b9268ef is a reordering away from
-- reporting `failed` as `empty`, which is the exact lie the Failed phase was
-- added to stop telling.
--
-- itemsFound and inferenceFailures are stored SEPARATELY and both feed
-- contract.TerminalPhase. abortReason is set only on the abort path, where the
-- phase is Failed directly WITHOUT consulting TerminalPhase.
CREATE TABLE scan (
    id                TEXT    PRIMARY KEY,
    phase             TEXT    NOT NULL,
    startedAt         TEXT    NOT NULL,
    finishedAt        TEXT,
    itemsFound        INTEGER NOT NULL DEFAULT 0,
    inferenceFailures INTEGER NOT NULL DEFAULT 0,
    callsUsed         INTEGER NOT NULL DEFAULT 0,
    callBudget        INTEGER NOT NULL DEFAULT 4,
    abortReason       TEXT
);

CREATE INDEX idx_scan_startedAt ON scan (startedAt);

-- One item a scan identified, before the user confirms it into inventory.
--
-- categoryRaw, not the model's `category`: the canonical rawValue is written at
-- the boundary where untrusted model text enters, so nothing downstream has to
-- re-normalize (categories.md § "Fallback rule", rules 4 and 5).
--
-- `count` is how many crops resolved to this row (InventoryLogic.Grouped.count),
-- which becomes the confirmed quantity.
CREATE TABLE scan_item (
    id            TEXT    PRIMARY KEY,
    scanId        TEXT    NOT NULL REFERENCES scan (id) ON DELETE CASCADE,
    position      INTEGER NOT NULL,
    name          TEXT    NOT NULL,
    brand         TEXT,
    categoryRaw   TEXT    NOT NULL,
    confidence    REAL    NOT NULL,
    count         INTEGER NOT NULL DEFAULT 1,
    sourcePhotoID TEXT
);

CREATE INDEX idx_scan_item_scanId ON scan_item (scanId);

-- A photo staged for a scan.
--
-- Identity is the UUID in `id`, never a position in a list. `position` exists
-- only to render the batch in the order it was captured; nothing addresses a
-- photo by it. Offset-based identity is the crash class BatchPhoto/PhotoBatch
-- were written to prevent on iOS (PhotoBatch.swift), and it is worse here
-- because the offsets are on the far side of an HTTP request.
--
-- `path` is relative to the photos root ($DD_STATE_DIR/photos), so moving the
-- state directory — which the SD-card write-amplification story requires — does
-- not rewrite every row.
CREATE TABLE photo (
    id        TEXT    PRIMARY KEY,
    scanId    TEXT    REFERENCES scan (id) ON DELETE SET NULL,
    path      TEXT    NOT NULL UNIQUE,
    byteSize  INTEGER NOT NULL,
    width     INTEGER NOT NULL,
    height    INTEGER NOT NULL,
    mimeType  TEXT    NOT NULL,
    position  INTEGER NOT NULL DEFAULT 0,
    createdAt TEXT    NOT NULL
);

CREATE INDEX idx_photo_scanId    ON photo (scanId);
CREATE INDEX idx_photo_createdAt ON photo (createdAt);

-- A pairing token, stored as a SHA-256 HASH and never as the token itself.
--
-- A 256-bit value from crypto/rand is not a password, so this is a plain
-- sha256 with no KDF and no salt — there is nothing to brute-force. What the
-- hash buys is that a stolen database file does not hand over a working
-- credential.
CREATE TABLE auth_token (
    id         TEXT PRIMARY KEY,
    tokenHash  TEXT NOT NULL UNIQUE,
    label      TEXT NOT NULL DEFAULT '',
    createdAt  TEXT NOT NULL,
    lastUsedAt TEXT,
    revokedAt  TEXT
);

-- Sticky runtime facts that must survive a restart: the CPU-fallback decision a
-- broken CUDA build earned, the resolved llama.cpp runtime variant, the last
-- TLS SAN set. Separate from `setting` because nothing here is a user
-- preference and none of it should appear on the Settings screen.
CREATE TABLE runtime_state (
    key       TEXT PRIMARY KEY,
    value     TEXT NOT NULL,
    updatedAt TEXT NOT NULL
);
