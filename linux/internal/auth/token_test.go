package auth

import (
	"encoding/base32"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func perm(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Mode().Perm()
}

func TestGenerateTokenShape(t *testing.T) {
	tok, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(tok)
	if err != nil {
		t.Fatalf("token is not unpadded base32: %v", err)
	}
	if len(raw) != TokenBytes {
		t.Errorf("token carries %d bytes, want %d", len(raw), TokenBytes)
	}
	if strings.ContainsAny(tok, "=0189") {
		t.Errorf("token %q contains padding or a digit outside the base32 alphabet (0, 1, 8 and 9 are excluded, which is also why it is hard to mistype)", tok)
	}
	if tok != strings.ToUpper(tok) {
		t.Errorf("token %q is not uppercase; it has to be typed into a phone", tok)
	}

	other, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if tok == other {
		t.Fatal("two generated tokens are identical")
	}
}

func TestHashToken(t *testing.T) {
	a := HashToken("ABC")
	if HashToken("ABC") != a {
		t.Error("HashToken is not deterministic")
	}
	if HashToken("ABD") == a {
		t.Error("different tokens hashed the same")
	}
	// A token pasted from a terminal often arrives with a newline.
	if HashToken("  ABC\n") != a {
		t.Error("surrounding whitespace must not change the hash")
	}
}

func TestEnsureCreatesOnceThenLoads(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "auth")
	s := NewTokenStore(dir)

	tok, created, err := s.Ensure()
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !created {
		t.Fatal("the first Ensure must create a token")
	}
	if tok == "" {
		t.Fatal("the token must be returned on creation; that is the one moment it can be printed")
	}

	// A second store over the same directory, as a restart would build.
	s2 := NewTokenStore(dir)
	tok2, created2, err := s2.Ensure()
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if created2 {
		t.Error("a restart must not replace the token")
	}
	if tok2 != "" {
		t.Error("a restart must not re-print the token into the journal")
	}
	if !s2.Verify(tok) {
		t.Error("the reloaded store must verify the original token")
	}
}

func TestHashFileHoldsNoPlaintext(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "auth")
	s := NewTokenStore(dir)
	tok, _, err := s.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, TokenHashFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), tok) {
		t.Fatal("the credential of record contains the token in the clear")
	}
	if len(strings.TrimSpace(string(data))) != 64 {
		t.Errorf("%s = %q, want 64 hex characters of SHA-256", TokenHashFileName, data)
	}
}

func TestEnsureFileModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "auth")
	s := NewTokenStore(dir)
	if _, _, err := s.Ensure(); err != nil {
		t.Fatal(err)
	}
	if got := perm(t, dir); got != DirMode {
		t.Errorf("directory mode = %#o, want %#o", got, DirMode)
	}
	for _, name := range []string{TokenHashFileName, TokenFileName} {
		if got := perm(t, filepath.Join(dir, name)); got != FileMode {
			t.Errorf("%s mode = %#o, want %#o", name, got, FileMode)
		}
	}
}

func TestLooseModesAreTightened(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "auth")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	s := NewTokenStore(dir)
	if _, err := s.Rotate(); err != nil {
		t.Fatal(err)
	}
	if got := perm(t, dir); got != DirMode {
		t.Errorf("directory mode = %#o, want %#o", got, DirMode)
	}
	for _, name := range []string{TokenHashFileName, TokenFileName} {
		if got := perm(t, filepath.Join(dir, name)); got != FileMode {
			t.Errorf("%s mode = %#o, want %#o", name, got, FileMode)
		}
	}
}

func TestVerify(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "auth")
	s := NewTokenStore(dir)

	if s.Verify("anything") {
		t.Error("a store with no token loaded must verify nothing")
	}
	if s.Loaded() {
		t.Error("Loaded must be false before Ensure")
	}

	tok, _, err := s.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if !s.Loaded() {
		t.Error("Loaded must be true after Ensure")
	}

	tests := []struct {
		name      string
		presented string
		want      bool
	}{
		{"the token", tok, true},
		{"trailing newline", tok + "\n", true},
		{"surrounding spaces", "  " + tok + "  ", true},
		{"empty", "", false},
		{"wrong", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", false},
		{"one character short", tok[:len(tok)-1], false},
		{"one character different", "A" + tok[1:], false},
		{"lowercased", strings.ToLower(tok), false},
		{"absurdly long", strings.Repeat("A", 100000), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.Verify(tc.presented); got != tc.want {
				t.Errorf("Verify = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestShow(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "auth")
	s := NewTokenStore(dir)
	tok, _, err := s.Ensure()
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.Show()
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got != tok {
		t.Errorf("Show = %q, want the created token", got)
	}
}

func TestShowAfterForget(t *testing.T) {
	// Deleting the local copy is a supported hardening step: authentication
	// keeps working, the token simply cannot be printed again.
	dir := filepath.Join(t.TempDir(), "auth")
	s := NewTokenStore(dir)
	tok, _, err := s.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Forget(); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Show(); !errors.Is(err, ErrTokenNotRecoverable) {
		t.Fatalf("Show = %v, want ErrTokenNotRecoverable", err)
	}
	if !s.Verify(tok) {
		t.Error("forgetting the printable copy must not break authentication")
	}
	if err := s.Forget(); err != nil {
		t.Errorf("Forget must be idempotent, got %v", err)
	}
}

func TestShowRefusesATokenThatWouldNotWork(t *testing.T) {
	// Printing a token that does not actually authenticate — after a hand edit
	// or a half-finished restore — sends the user to debug the wrong thing.
	dir := filepath.Join(t.TempDir(), "auth")
	s := NewTokenStore(dir)
	if _, _, err := s.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, TokenFileName), []byte("SOMETHINGELSE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Show(); err == nil {
		t.Fatal("Show must refuse a token that does not match the stored hash")
	}
}

func TestRotate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "auth")
	s := NewTokenStore(dir)
	old, _, err := s.Ensure()
	if err != nil {
		t.Fatal(err)
	}

	fresh, err := s.Rotate()
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if fresh == old {
		t.Fatal("Rotate returned the same token")
	}
	if s.Verify(old) {
		t.Error("the old token must stop working immediately")
	}
	if !s.Verify(fresh) {
		t.Error("the new token must work")
	}
	shown, err := s.Show()
	if err != nil || shown != fresh {
		t.Errorf("Show = (%q, %v), want the rotated token", shown, err)
	}
}

func TestEnsureRejectsACorruptHashFile(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"not hex", "zzzz\n"},
		{"too short", "abcd\n"},
		{"empty", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "auth")
			if err := os.MkdirAll(dir, DirMode); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, TokenHashFileName), []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			s := NewTokenStore(dir)
			_, _, err := s.Ensure()
			if err == nil {
				t.Fatal("a corrupt hash file must fail loudly, not quietly mint a new token that invalidates every paired device")
			}
			if !strings.Contains(err.Error(), TokenHashFileName) {
				t.Errorf("error %q does not name the file to delete", err)
			}
		})
	}
}

func TestTokenStoreNeedsADirectory(t *testing.T) {
	s := NewTokenStore("")
	if _, _, err := s.Ensure(); err == nil {
		t.Fatal("a store with no directory must fail rather than write somewhere surprising")
	}
}
