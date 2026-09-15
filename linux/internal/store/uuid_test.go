package store

import (
	"regexp"
	"testing"
)

var uuidV4Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewUUIDShapeAndBits(t *testing.T) {
	// The version nibble and the variant bits are the whole reason this is not
	// just 128 random bits rendered with hyphens: a strict parser on the other
	// end rejects a UUID whose version is not 4.
	for i := 0; i < 256; i++ {
		got, err := newUUID()
		if err != nil {
			t.Fatalf("newUUID: %v", err)
		}
		if !uuidV4Pattern.MatchString(got) {
			t.Fatalf("newUUID = %q, want a canonical lower-case v4 UUID", got)
		}
	}
}

func TestNewUUIDIsUnique(t *testing.T) {
	seen := make(map[string]struct{}, 4096)
	for i := 0; i < 4096; i++ {
		got, err := newUUID()
		if err != nil {
			t.Fatalf("newUUID: %v", err)
		}
		if _, dup := seen[got]; dup {
			t.Fatalf("newUUID returned %q twice in %d draws", got, i+1)
		}
		seen[got] = struct{}{}
	}
}
