package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTestPhotos(t *testing.T) (*DB, *PhotoStore) {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "dinnerdecider.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	photos, err := NewPhotoStore(db, dir)
	if err != nil {
		t.Fatalf("NewPhotoStore: %v", err)
	}
	return db, photos
}

func stage(t *testing.T, p *PhotoStore, body string) Photo {
	t.Helper()
	photo, err := p.Stage(context.Background(), []byte(body),
		PhotoMeta{Width: 2016, Height: 1512, MimeType: "image/jpeg"})
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	return photo
}

// These are photographs of the inside of someone's home. The directory is
// owner-only and so is every file in it.
func TestPhotoPermissions(t *testing.T) {
	_, photos := openTestPhotos(t)
	photo := stage(t, photos, "jpeg-bytes")

	dirInfo, err := os.Stat(photos.Root())
	if err != nil {
		t.Fatalf("stat root: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("photos directory mode = %o, want 700", got)
	}
	fileInfo, err := os.Stat(photos.AbsPath(photo))
	if err != nil {
		t.Fatalf("stat photo: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("photo file mode = %o, want 600", got)
	}
}

// NewPhotoStore must tighten a directory that already exists with looser
// permissions — MkdirAll leaves an existing directory alone.
func TestNewPhotoStoreTightensAnExistingDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "photos"), 0o755); err != nil {
		t.Fatalf("pre-creating: %v", err)
	}
	db, err := Open(filepath.Join(dir, "dinnerdecider.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	photos, err := NewPhotoStore(db, dir)
	if err != nil {
		t.Fatalf("NewPhotoStore: %v", err)
	}
	info, err := os.Stat(photos.Root())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("mode = %o, want 700", got)
	}
}

func TestStageAndReadBack(t *testing.T) {
	_, photos := openTestPhotos(t)
	ctx := context.Background()

	photo := stage(t, photos, "the-bytes")
	if photo.ID == "" {
		t.Fatal("Stage returned an empty id")
	}
	if !uuidV4Pattern.MatchString(photo.ID) {
		t.Errorf("id = %q, want a v4 UUID", photo.ID)
	}
	if photo.ScanID != nil {
		t.Errorf("a freshly staged photo has scanId = %v, want nil", *photo.ScanID)
	}
	if photo.ByteSize != int64(len("the-bytes")) {
		t.Errorf("byteSize = %d", photo.ByteSize)
	}
	// The file name is the id plus an extension from a fixed table, never the
	// caller's content-type string.
	if photo.Path != photo.ID+".jpg" {
		t.Errorf("path = %q, want %q", photo.Path, photo.ID+".jpg")
	}

	data, err := photos.ReadBytes(ctx, photo.ID)
	if err != nil {
		t.Fatalf("ReadBytes: %v", err)
	}
	if string(data) != "the-bytes" {
		t.Errorf("ReadBytes = %q", data)
	}

	got, err := photos.Get(ctx, photo.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Width != 2016 || got.Height != 1512 || got.MimeType != "image/jpeg" {
		t.Errorf("metadata = %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Error("createdAt was not set")
	}

	if _, err := photos.Get(ctx, "no-such-photo"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get on a missing id = %v, want ErrNotFound", err)
	}
	if _, err := photos.ReadBytes(ctx, "no-such-photo"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ReadBytes on a missing id = %v, want ErrNotFound", err)
	}
}

func TestExtensionComesFromAFixedTable(t *testing.T) {
	_, photos := openTestPhotos(t)
	ctx := context.Background()
	cases := map[string]string{
		"image/jpeg":                ".jpg",
		"image/png":                 ".png",
		"image/webp":                ".webp",
		"application/octet-stream":  ".bin",
		"image/../../etc/passwd":    ".bin",
		"image/png; charset=binary": ".bin",
	}
	for mime, wantExt := range cases {
		photo, err := photos.Stage(ctx, []byte("x"), PhotoMeta{MimeType: mime})
		if err != nil {
			t.Fatalf("Stage(%q): %v", mime, err)
		}
		if filepath.Ext(photo.Path) != wantExt {
			t.Errorf("Stage(%q) path = %q, want extension %q", mime, photo.Path, wantExt)
		}
		// Nothing the caller supplies may escape the photos directory.
		if strings.Contains(photo.Path, "/") || strings.Contains(photo.Path, "..") {
			t.Errorf("Stage(%q) produced a path with a separator: %q", mime, photo.Path)
		}
	}
}

// Removal by a stale identifier must be a no-op, never a trap. This is the
// crash class UUID identity exists to prevent: a double tap, a retried request,
// or a client holding a list it fetched ten seconds ago.
func TestRemoveIsIdempotentAndNeverTraps(t *testing.T) {
	_, photos := openTestPhotos(t)
	ctx := context.Background()

	photo := stage(t, photos, "bytes")
	if err := photos.Remove(ctx, photo.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	// Second removal of the same id: gone already, and that is fine.
	if err := photos.Remove(ctx, photo.ID); err != nil {
		t.Errorf("second Remove = %v, want nil", err)
	}
	// An id that never existed, and an id-shaped string that never will.
	for _, id := range []string{"", "3", "00000000-0000-4000-8000-000000000000"} {
		if err := photos.Remove(ctx, id); err != nil {
			t.Errorf("Remove(%q) = %v, want nil", id, err)
		}
	}
	if _, err := os.Stat(photos.AbsPath(photo)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the file survived Remove: %v", err)
	}
	staged, _ := photos.ListStaged(ctx)
	if len(staged) != 0 {
		t.Errorf("staged = %d, want 0", len(staged))
	}
}

// Position is display order, never identity. Removing the first photo must not
// renumber anyone else into its place — that is precisely how an index-keyed
// store deletes the wrong photo.
func TestIdentityIsTheUUIDNotThePosition(t *testing.T) {
	_, photos := openTestPhotos(t)
	ctx := context.Background()

	first := stage(t, photos, "one")
	second := stage(t, photos, "two")
	third := stage(t, photos, "three")
	if first.Position != 0 || second.Position != 1 || third.Position != 2 {
		t.Fatalf("positions = %d, %d, %d", first.Position, second.Position, third.Position)
	}

	if err := photos.Remove(ctx, first.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	staged, err := photos.ListStaged(ctx)
	if err != nil {
		t.Fatalf("ListStaged: %v", err)
	}
	if len(staged) != 2 {
		t.Fatalf("staged = %d, want 2", len(staged))
	}
	if staged[0].ID != second.ID || staged[1].ID != third.ID {
		t.Errorf("identities shifted after a removal: %v", []string{staged[0].ID, staged[1].ID})
	}
	// The surviving photos keep their own bytes, which is the observable
	// consequence of identity being stable.
	data, err := photos.ReadBytes(ctx, second.ID)
	if err != nil || string(data) != "two" {
		t.Errorf("second photo = %q, %v", data, err)
	}
}

func TestListStagedIsNeverNilAndIsOrdered(t *testing.T) {
	_, photos := openTestPhotos(t)
	ctx := context.Background()

	staged, err := photos.ListStaged(ctx)
	if err != nil {
		t.Fatalf("ListStaged: %v", err)
	}
	if staged == nil {
		t.Error("an empty batch returned a nil slice; it marshals to JSON null")
	}

	a := stage(t, photos, "a")
	b := stage(t, photos, "b")
	staged, _ = photos.ListStaged(ctx)
	if len(staged) != 2 || staged[0].ID != a.ID || staged[1].ID != b.ID {
		t.Errorf("order = %v", staged)
	}
}

func TestAttachToScanMovesPhotosOutOfStaging(t *testing.T) {
	_, photos := openTestPhotos(t)
	ctx := context.Background()

	a := stage(t, photos, "a")
	b := stage(t, photos, "b")
	if _, err := photos.db.SQL().Exec(
		`INSERT INTO scan (id, phase, startedAt) VALUES ('scan-1', 'scanning', ?)`,
		formatTime(time.Now())); err != nil {
		t.Fatalf("insert scan: %v", err)
	}
	// Attach in reverse order: the given order is the batch order.
	if err := photos.AttachToScan(ctx, "scan-1", []string{b.ID, a.ID}); err != nil {
		t.Fatalf("AttachToScan: %v", err)
	}

	staged, _ := photos.ListStaged(ctx)
	if len(staged) != 0 {
		t.Errorf("staged = %d after attaching, want 0", len(staged))
	}
	forScan, err := photos.ListForScan(ctx, "scan-1")
	if err != nil {
		t.Fatalf("ListForScan: %v", err)
	}
	if len(forScan) != 2 || forScan[0].ID != b.ID || forScan[1].ID != a.ID {
		t.Errorf("scan order = %v", forScan)
	}
	if forScan[0].ScanID == nil || *forScan[0].ScanID != "scan-1" {
		t.Errorf("scanId = %v", forScan[0].ScanID)
	}
}

// A stale id in the attach list is skipped, not fatal: refusing to start a scan
// over one id the user already deleted is the same trap Remove avoids.
func TestAttachToScanSkipsStaleIDs(t *testing.T) {
	_, photos := openTestPhotos(t)
	ctx := context.Background()
	if _, err := photos.db.SQL().Exec(
		`INSERT INTO scan (id, phase, startedAt) VALUES ('scan-1', 'scanning', ?)`,
		formatTime(time.Now())); err != nil {
		t.Fatalf("insert scan: %v", err)
	}
	a := stage(t, photos, "a")
	if err := photos.AttachToScan(ctx, "scan-1", []string{"gone", a.ID}); err != nil {
		t.Fatalf("AttachToScan: %v", err)
	}
	forScan, _ := photos.ListForScan(ctx, "scan-1")
	if len(forScan) != 1 || forScan[0].ID != a.ID {
		t.Errorf("attached = %v", forScan)
	}
}

// Retention, half one: a scan's photos go at confirm or discard. Waiting for
// the 24-hour sweep would leave a fridge interior on disk for a day after the
// user had moved on.
func TestDeleteForScanRemovesBytesAndRows(t *testing.T) {
	_, photos := openTestPhotos(t)
	ctx := context.Background()
	if _, err := photos.db.SQL().Exec(
		`INSERT INTO scan (id, phase, startedAt) VALUES ('scan-1', 'scanning', ?)`,
		formatTime(time.Now())); err != nil {
		t.Fatalf("insert scan: %v", err)
	}
	a := stage(t, photos, "a")
	b := stage(t, photos, "b")
	keep := stage(t, photos, "keep")
	if err := photos.AttachToScan(ctx, "scan-1", []string{a.ID, b.ID}); err != nil {
		t.Fatalf("AttachToScan: %v", err)
	}

	n, err := photos.DeleteForScan(ctx, "scan-1")
	if err != nil {
		t.Fatalf("DeleteForScan: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted %d, want 2", n)
	}
	for _, photo := range []Photo{a, b} {
		if _, err := os.Stat(photos.AbsPath(photo)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("file for %s survived: %v", photo.ID, err)
		}
		if _, err := photos.Get(ctx, photo.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("row for %s survived: %v", photo.ID, err)
		}
	}
	// Unrelated staged photos are untouched.
	if _, err := photos.Get(ctx, keep.ID); err != nil {
		t.Errorf("an unrelated staged photo was deleted: %v", err)
	}
	// Deleting a scan with no photos is a no-op.
	if n, err := photos.DeleteForScan(ctx, "scan-none"); err != nil || n != 0 {
		t.Errorf("DeleteForScan on nothing = %d, %v", n, err)
	}
}

// Retention, half two: anything that slipped through goes after 24 hours.
func TestSweepExpired(t *testing.T) {
	_, photos := openTestPhotos(t)
	ctx := context.Background()

	old := stage(t, photos, "old")
	fresh := stage(t, photos, "fresh")
	// Age the row past the default retention window.
	if _, err := photos.db.SQL().Exec(`UPDATE photo SET createdAt = ? WHERE id = ?`,
		formatTime(time.Now().Add(-25*time.Hour)), old.ID); err != nil {
		t.Fatalf("ageing the row: %v", err)
	}

	// Zero means DefaultPhotoRetention, so a caller that forgot to configure a
	// window gets the 24-hour policy rather than deleting everything.
	n, err := photos.SweepExpired(ctx, 0)
	if err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}
	if n != 1 {
		t.Errorf("swept %d, want 1", n)
	}
	if _, err := photos.Get(ctx, old.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the expired row survived: %v", err)
	}
	if _, err := os.Stat(photos.AbsPath(old)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the expired file survived: %v", err)
	}
	if _, err := photos.Get(ctx, fresh.ID); err != nil {
		t.Errorf("a fresh photo was swept: %v", err)
	}
}

func TestSweepExpiredHonoursAnExplicitWindow(t *testing.T) {
	_, photos := openTestPhotos(t)
	ctx := context.Background()
	photo := stage(t, photos, "x")
	if _, err := photos.db.SQL().Exec(`UPDATE photo SET createdAt = ? WHERE id = ?`,
		formatTime(time.Now().Add(-2*time.Hour)), photo.ID); err != nil {
		t.Fatalf("ageing: %v", err)
	}
	// Two hours old: inside the default window, outside a one-hour one.
	if n, err := photos.SweepExpired(ctx, DefaultPhotoRetention); err != nil || n != 0 {
		t.Errorf("default window swept %d, %v; want 0", n, err)
	}
	if n, err := photos.SweepExpired(ctx, time.Hour); err != nil || n != 1 {
		t.Errorf("one-hour window swept %d, %v; want 1", n, err)
	}
}

// The orphan pass is the half that is easy to leave out, and it is where a leak
// hides: a crash between writing bytes and inserting the row leaves a file no
// row-driven sweep will ever visit.
func TestSweepExpiredRemovesOrphanFiles(t *testing.T) {
	_, photos := openTestPhotos(t)
	ctx := context.Background()

	oldOrphan := filepath.Join(photos.Root(), "orphan-old.jpg")
	if err := os.WriteFile(oldOrphan, []byte("leaked"), 0o600); err != nil {
		t.Fatalf("writing orphan: %v", err)
	}
	stale := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(oldOrphan, stale, stale); err != nil {
		t.Fatalf("ageing orphan: %v", err)
	}
	// A fresh orphan is a file a concurrent Stage may be in the middle of
	// writing; sweeping it would be a race against ourselves.
	freshOrphan := filepath.Join(photos.Root(), "orphan-new.jpg")
	if err := os.WriteFile(freshOrphan, []byte("in flight"), 0o600); err != nil {
		t.Fatalf("writing orphan: %v", err)
	}
	tracked := stage(t, photos, "tracked")

	n, err := photos.SweepExpired(ctx, DefaultPhotoRetention)
	if err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}
	if n != 1 {
		t.Errorf("swept %d, want 1", n)
	}
	if _, err := os.Stat(oldOrphan); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the stale orphan survived: %v", err)
	}
	if _, err := os.Stat(freshOrphan); err != nil {
		t.Errorf("a fresh orphan was swept: %v", err)
	}
	if _, err := os.Stat(photos.AbsPath(tracked)); err != nil {
		t.Errorf("a tracked photo was swept as an orphan: %v", err)
	}
}

// The filesystem path is not part of the wire shape: handing a client a path
// invites it to ask for a different one.
func TestPhotoJSONOmitsThePath(t *testing.T) {
	_, photos := openTestPhotos(t)
	photo := stage(t, photos, "bytes")

	data, err := json.Marshal(photo)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := raw["path"]; ok {
		t.Errorf("photo JSON exposes the filesystem path: %s", data)
	}
	if _, ok := raw["id"]; !ok {
		t.Errorf("photo JSON has no id: %s", data)
	}
}

func TestDefaultStateDir(t *testing.T) {
	t.Setenv("DD_STATE_DIR", "/srv/dd")
	if got := DefaultStateDir(); got != "/srv/dd" {
		t.Errorf("DefaultStateDir = %q, want /srv/dd", got)
	}
	t.Setenv("DD_STATE_DIR", "   ")
	if got := DefaultStateDir(); got != "/var/lib/dinnerdecider" {
		t.Errorf("DefaultStateDir with a blank env = %q, want the fallback", got)
	}
	t.Setenv("DD_STATE_DIR", "")
	if got := DefaultStateDir(); got != "/var/lib/dinnerdecider" {
		t.Errorf("DefaultStateDir with no env = %q, want the fallback", got)
	}
}
