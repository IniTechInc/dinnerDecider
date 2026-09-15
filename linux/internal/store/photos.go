package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultPhotoRetention is how long a staged photo survives without being
// swept.
//
// Twenty-four hours, and it is implemented rather than documented. These are
// photographs of the inside of someone's home, taken on a device that sits on a
// shelf and is never looked at again; "we intend to add cleanup" means the
// fridge photos from last March are still on the SD card. A scan's photos go at
// confirm or discard (DeleteForScan); anything that slipped through — an
// abandoned upload, a crash between staging and scanning, a scan the user never
// came back to — goes here.
const DefaultPhotoRetention = 24 * time.Hour

// photoDirPerm and photoFilePerm keep the photos owner-only.
//
// Explicitly chmodded after creation rather than trusted to the mode argument,
// because the process umask masks the mode passed to MkdirAll and OpenFile, and
// MkdirAll does not touch the permissions of a directory that already exists —
// so a directory created earlier by something more relaxed would stay relaxed.
const (
	photoDirPerm  os.FileMode = 0o700
	photoFilePerm os.FileMode = 0o600
)

// DefaultStateDir is $DD_STATE_DIR, or /var/lib/dinnerdecider when unset.
//
// A convenience for callers that have not built a config yet; the config
// package owns the real precedence chain (defaults, file, env, flags) and
// should pass its resolved value to NewPhotoStore. Relocatable on purpose: SD
// card write amplification is a real IoT failure mode, and moving the state
// directory to a USB SSD is the fix.
func DefaultStateDir() string {
	if dir := strings.TrimSpace(os.Getenv("DD_STATE_DIR")); dir != "" {
		return dir
	}
	return "/var/lib/dinnerdecider"
}

// Photo is one staged or scanned photo.
//
// Identity is ID, a UUID, and nothing else. Position exists only so the batch
// renders in capture order.
type Photo struct {
	ID     string  `json:"id"`
	ScanID *string `json:"scanId"`
	// Path is relative to the photos root, so relocating the state directory
	// does not rewrite every row. Not on the wire: the bytes are served through
	// the API by id, and handing a client a filesystem path invites it to ask
	// for a different one.
	Path      string    `json:"-"`
	ByteSize  int64     `json:"byteSize"`
	Width     int       `json:"width"`
	Height    int       `json:"height"`
	MimeType  string    `json:"mimeType"`
	Position  int       `json:"position"`
	CreatedAt time.Time `json:"createdAt"`
}

// PhotoMeta is what the caller knows about an image it has already decoded.
type PhotoMeta struct {
	Width    int
	Height   int
	MimeType string
}

// PhotoStore stages photos: rows in the database, bytes under
// $DD_STATE_DIR/photos.
type PhotoStore struct {
	db   *DB
	root string
}

// NewPhotoStore prepares the photos directory under stateDir.
func NewPhotoStore(db *DB, stateDir string) (*PhotoStore, error) {
	root := filepath.Join(stateDir, "photos")
	if err := os.MkdirAll(root, photoDirPerm); err != nil {
		return nil, fmt.Errorf("store: creating photos directory: %w", err)
	}
	if err := os.Chmod(root, photoDirPerm); err != nil {
		return nil, fmt.Errorf("store: securing photos directory: %w", err)
	}
	return &PhotoStore{db: db, root: root}, nil
}

// Root is the absolute photos directory.
func (p *PhotoStore) Root() string { return p.root }

// AbsPath is where a photo's bytes live.
func (p *PhotoStore) AbsPath(photo Photo) string { return filepath.Join(p.root, photo.Path) }

// extensionFor maps a sniffed content type to a file extension.
//
// A fixed table, never the caller's string: the extension becomes part of a
// filesystem path, and the only safe way to keep a caller-supplied value out of
// a path is not to put it there.
func extensionFor(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	default:
		return ".bin"
	}
}

const photoColumns = `id, scanId, path, byteSize, width, height, mimeType, position, createdAt`

func scanPhoto(s interface{ Scan(...any) error }) (Photo, error) {
	var (
		photo     Photo
		scanID    sql.NullString
		createdAt string
	)
	if err := s.Scan(&photo.ID, &scanID, &photo.Path, &photo.ByteSize, &photo.Width,
		&photo.Height, &photo.MimeType, &photo.Position, &createdAt); err != nil {
		return Photo{}, err
	}
	if scanID.Valid {
		photo.ScanID = &scanID.String
	}
	parsed, err := parseTime(createdAt)
	if err != nil {
		return Photo{}, err
	}
	photo.CreatedAt = parsed
	return photo, nil
}

// Stage writes one photo's bytes and records it, unattached to any scan.
//
// The bytes land first and the row second: a file with no row is reachable by
// the orphan sweep, while a row with no file is a 500 every time anyone asks
// for it. If the row fails to insert, the file is removed again.
func (p *PhotoStore) Stage(ctx context.Context, data []byte, meta PhotoMeta) (Photo, error) {
	id, err := newUUID()
	if err != nil {
		return Photo{}, err
	}
	name := id + extensionFor(meta.MimeType)
	abs := filepath.Join(p.root, name)

	// O_EXCL so a colliding name can never silently overwrite someone else's
	// photo. With a v4 UUID this cannot happen; the flag is what makes that a
	// guarantee rather than an expectation.
	f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL, photoFilePerm)
	if err != nil {
		return Photo{}, fmt.Errorf("store: creating photo file: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(abs)
		return Photo{}, fmt.Errorf("store: writing photo file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(abs)
		return Photo{}, fmt.Errorf("store: closing photo file: %w", err)
	}
	if err := os.Chmod(abs, photoFilePerm); err != nil {
		_ = os.Remove(abs)
		return Photo{}, fmt.Errorf("store: securing photo file: %w", err)
	}

	photo := Photo{
		ID:        id,
		Path:      name,
		ByteSize:  int64(len(data)),
		Width:     meta.Width,
		Height:    meta.Height,
		MimeType:  meta.MimeType,
		CreatedAt: time.Now(),
	}
	err = p.db.WithTx(ctx, func(tx *sql.Tx) error {
		// Position is the next slot among the currently staged photos. It is
		// display order and nothing else — never an identity, never an address.
		var next sql.NullInt64
		if err := tx.QueryRowContext(ctx,
			`SELECT max(position) FROM photo WHERE scanId IS NULL`).Scan(&next); err != nil {
			return fmt.Errorf("store: numbering staged photo: %w", err)
		}
		if next.Valid {
			photo.Position = int(next.Int64) + 1
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO photo (`+photoColumns+`) VALUES (?, NULL, ?, ?, ?, ?, ?, ?, ?)`,
			photo.ID, photo.Path, photo.ByteSize, photo.Width, photo.Height,
			photo.MimeType, photo.Position, formatTime(photo.CreatedAt))
		if err != nil {
			return fmt.Errorf("store: recording photo: %w", err)
		}
		return nil
	})
	if err != nil {
		_ = os.Remove(abs)
		return Photo{}, err
	}
	return photo, nil
}

// Get returns one photo's record by id, or ErrNotFound.
func (p *PhotoStore) Get(ctx context.Context, id string) (Photo, error) {
	photo, err := scanPhoto(p.db.sql.QueryRowContext(ctx,
		`SELECT `+photoColumns+` FROM photo WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Photo{}, fmt.Errorf("store: photo %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return Photo{}, fmt.Errorf("store: reading photo %q: %w", id, err)
	}
	return photo, nil
}

// ReadBytes returns one photo's bytes.
func (p *PhotoStore) ReadBytes(ctx context.Context, id string) ([]byte, error) {
	photo, err := p.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p.AbsPath(photo))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("store: photo %q has no file: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: reading photo %q: %w", id, err)
	}
	return data, nil
}

// ListStaged returns the photos not yet attached to a scan, in capture order.
// Never nil.
func (p *PhotoStore) ListStaged(ctx context.Context) ([]Photo, error) {
	return p.query(ctx,
		`SELECT `+photoColumns+` FROM photo WHERE scanId IS NULL ORDER BY position, createdAt, id`)
}

// ListForScan returns one scan's photos, in capture order. Never nil.
func (p *PhotoStore) ListForScan(ctx context.Context, scanID string) ([]Photo, error) {
	return p.query(ctx,
		`SELECT `+photoColumns+` FROM photo WHERE scanId = ? ORDER BY position, createdAt, id`, scanID)
}

func (p *PhotoStore) query(ctx context.Context, q string, args ...any) ([]Photo, error) {
	rows, err := p.db.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: listing photos: %w", err)
	}
	defer rows.Close()
	out := []Photo{}
	for rows.Next() {
		photo, err := scanPhoto(rows)
		if err != nil {
			return nil, fmt.Errorf("store: listing photos: %w", err)
		}
		out = append(out, photo)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: listing photos: %w", err)
	}
	return out, nil
}

// Remove deletes one photo by id, bytes and row.
//
// IDEMPOTENT. Removing a photo that is already gone is a no-op that returns
// nil, never ErrNotFound and never a trap. That is the whole reason identity is
// a UUID and not a list position: a double tap, a retried request, or a client
// holding a list it fetched ten seconds ago must not be able to delete the
// wrong photo or blow up. It is the crash class BatchPhoto/PhotoBatch were
// written to prevent on iOS (PhotoBatch.remove(id:) "removes all" precisely so
// a missing id is harmless), and it is worse here because the stale state is on
// the far side of an HTTP request.
//
// The file goes first. If it cannot be removed for any reason other than being
// absent, the row stays and the error is returned, so the photo is still
// accounted for and the operation is retryable — a row-first order would leave
// bytes on disk with nothing pointing at them, which for photos of someone's
// home is the failure that matters.
func (p *PhotoStore) Remove(ctx context.Context, id string) error {
	photo, err := p.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := removeFile(p.AbsPath(photo)); err != nil {
		return err
	}
	if _, err := p.db.sql.ExecContext(ctx, `DELETE FROM photo WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: deleting photo %q: %w", id, err)
	}
	return nil
}

// AttachToScan claims staged photos for a scan, in the order given.
//
// Ids that no longer exist are skipped rather than failing the whole scan: the
// caller may be replaying a list a client assembled before the user deleted a
// thumbnail, and refusing to start a scan over one stale id would be the same
// trap Remove exists to avoid.
func (p *PhotoStore) AttachToScan(ctx context.Context, scanID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return p.db.WithTx(ctx, func(tx *sql.Tx) error {
		for i, id := range ids {
			if _, err := tx.ExecContext(ctx,
				`UPDATE photo SET scanId = ?, position = ? WHERE id = ?`, scanID, i, id); err != nil {
				return fmt.Errorf("store: attaching photo %q to scan %q: %w", id, scanID, err)
			}
		}
		return nil
	})
}

// DeleteForScan removes every photo belonging to a scan, bytes and rows, and
// reports how many went.
//
// Called on confirm and on discard — the two moments a scan's photos stop being
// needed. Waiting for the 24-hour sweep instead would leave a fridge interior
// on the disk for a day after the user had already moved on.
func (p *PhotoStore) DeleteForScan(ctx context.Context, scanID string) (int, error) {
	photos, err := p.ListForScan(ctx, scanID)
	if err != nil {
		return 0, err
	}
	return p.deleteAll(ctx, photos)
}

// SweepExpired removes photos older than olderThan, plus any files in the
// directory with no row at all, and reports how many went.
//
// A zero or negative duration means DefaultPhotoRetention, so a caller that
// forgot to configure it gets the 24-hour policy rather than deleting
// everything it can see.
//
// The orphan pass is the half that is easy to leave out and is exactly where a
// leak hides: a crash between writing bytes and inserting the row, or a row
// deleted by hand, leaves a file no row-driven sweep will ever visit.
func (p *PhotoStore) SweepExpired(ctx context.Context, olderThan time.Duration) (int, error) {
	if olderThan <= 0 {
		olderThan = DefaultPhotoRetention
	}
	cutoff := time.Now().Add(-olderThan)

	expired, err := p.query(ctx,
		`SELECT `+photoColumns+` FROM photo WHERE createdAt < ?`, formatTime(cutoff))
	if err != nil {
		return 0, err
	}
	removed, err := p.deleteAll(ctx, expired)
	if err != nil {
		return removed, err
	}

	orphans, err := p.sweepOrphanFiles(ctx, cutoff)
	return removed + orphans, err
}

// sweepOrphanFiles removes files in the photos directory that no row refers to
// and that are older than the cutoff.
//
// The age check matters: a file being written right now by a concurrent Stage
// has no row yet, and deleting it would be a race against ourselves.
func (p *PhotoStore) sweepOrphanFiles(ctx context.Context, cutoff time.Time) (int, error) {
	entries, err := os.ReadDir(p.root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("store: listing photos directory: %w", err)
	}

	known := map[string]struct{}{}
	rows, err := p.db.sql.QueryContext(ctx, `SELECT path FROM photo`)
	if err != nil {
		return 0, fmt.Errorf("store: listing photo paths: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return 0, fmt.Errorf("store: listing photo paths: %w", err)
		}
		known[path] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("store: listing photo paths: %w", err)
	}

	removed := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if _, ok := known[entry.Name()]; ok {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return removed, fmt.Errorf("store: inspecting orphan %q: %w", entry.Name(), err)
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		if err := removeFile(filepath.Join(p.root, entry.Name())); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// deleteAll removes a set of photos, bytes first then rows, in one transaction
// for the rows.
func (p *PhotoStore) deleteAll(ctx context.Context, photos []Photo) (int, error) {
	if len(photos) == 0 {
		return 0, nil
	}
	for _, photo := range photos {
		if err := removeFile(p.AbsPath(photo)); err != nil {
			return 0, err
		}
	}
	err := p.db.WithTx(ctx, func(tx *sql.Tx) error {
		for _, photo := range photos {
			if _, err := tx.ExecContext(ctx, `DELETE FROM photo WHERE id = ?`, photo.ID); err != nil {
				return fmt.Errorf("store: deleting photo %q: %w", photo.ID, err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(photos), nil
}

// removeFile deletes a file, treating "already gone" as success.
func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("store: removing %q: %w", path, err)
	}
	return nil
}
