package store

import (
	"crypto/rand"
	"fmt"
)

// newUUID returns a random RFC 4122 version 4 UUID in the canonical
// 8-4-4-4-12 lower-case hyphenated form.
//
// Hand-rolled on purpose. github.com/google/uuid WILL show up in go.sum as an
// indirect dependency of modernc.org/sqlite, and a reader grepping for it will
// find it — that is expected, and it is not a licence to import it. go.mod is
// frozen after the foundation task, the whole generator is fifteen lines of
// crypto/rand, and a dependency taken for fifteen lines is a dependency taken
// for its supply chain too. See the plan §1.1 and docs/DEPENDENCIES.md.
//
// crypto/rand, never math/rand: these identify a user's photos and inventory
// rows over a LAN-reachable API, so guessable identifiers are an enumeration
// vector, not a style preference.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("store: generating uuid: %w", err)
	}
	// Version 4 in the high nibble of octet 6, RFC 4122 variant (10xx) in the
	// high bits of octet 8. Without these the value is 128 random bits that
	// merely look like a UUID, and any strict parser on the other end rejects
	// it.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
