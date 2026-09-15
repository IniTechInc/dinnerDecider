package web

import (
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
)

// textAssetExts are the embedded file types a browser parses for references.
// A font or a licence file is never a source of a network request.
var textAssetExts = map[string]bool{
	".html": true,
	".css":  true,
	".js":   true,
	".svg":  true,
}

// svgNamespace is the one permitted occurrence of a `//` sequence in the tree.
//
// It is the SVG XML namespace identifier. A standalone `.svg` served as
// image/svg+xml is parsed as XML and will not render without it, and no browser
// ever dereferences it. Every other occurrence is a genuine external origin and
// the Content-Security-Policy (`default-src 'self'`) would block it at runtime.
const svgNamespace = `http://www.w3.org/2000/svg`

// schemeRelative catches `src="//host/x"` and friends, which inherit the page
// scheme and are just as external as an explicit `https:` prefix.
var schemeRelative = regexp.MustCompile(`["'(=]\s*//`)

func walkTextAssets(t *testing.T, fn func(name, body string)) {
	t.Helper()
	root := Static()
	err := fs.WalkDir(root, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !textAssetExts[strings.ToLower(path.Ext(name))] {
			return nil
		}
		raw, err := fs.ReadFile(root, name)
		if err != nil {
			return err
		}
		fn(name, string(raw))
		return nil
	})
	if err != nil {
		t.Fatalf("walking the embedded tree: %v", err)
	}
}

// TestNoExternalOrigins is the zero-CDN requirement made enforceable.
func TestNoExternalOrigins(t *testing.T) {
	seen := 0
	walkTextAssets(t, func(name, body string) {
		seen++
		stripped := body
		if strings.ToLower(path.Ext(name)) == ".svg" {
			stripped = strings.ReplaceAll(stripped, svgNamespace, "svg-namespace")
		}
		for _, scheme := range []string{"http://", "https://"} {
			if idx := strings.Index(stripped, scheme); idx >= 0 {
				t.Errorf("%s: absolute URL %q at byte %d; the CSP is default-src 'self', so this cannot load", name, excerpt(stripped, idx), idx)
			}
		}
		if loc := schemeRelative.FindStringIndex(stripped); loc != nil {
			t.Errorf("%s: scheme-relative URL %q at byte %d; it is still an external origin", name, excerpt(stripped, loc[0]), loc[0])
		}
	})
	if seen == 0 {
		t.Fatal("no text assets found in the embedded tree; the go:embed directive is not doing its job")
	}
}

// TestViewportMeta guards the single line without which the whole mobile-first
// stylesheet renders at desktop width on the phone this UI exists for.
func TestViewportMeta(t *testing.T) {
	raw, err := IndexHTML()
	if err != nil {
		t.Fatalf("reading index.html: %v", err)
	}
	body := string(raw)
	const want = `<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">`
	if !strings.Contains(body, want) {
		t.Errorf("index.html is missing the viewport meta tag:\n\twant %s", want)
	}
}

// TestShellListsEveryModule pins the "listed up front" rule: the shell declares
// every stylesheet and every module, so a feature file never has to edit it.
func TestShellListsEveryModule(t *testing.T) {
	raw, err := IndexHTML()
	if err != nil {
		t.Fatalf("reading index.html: %v", err)
	}
	body := string(raw)
	for _, module := range []string{
		"js/api.js", "js/shell.js", "js/capture.js", "js/scan.js",
		"js/inventory.js", "js/recipes.js", "js/shopping.js", "js/settings.js",
	} {
		want := `<script type="module" src="` + module + `"></script>`
		if !strings.Contains(body, want) {
			t.Errorf("index.html does not list %s up front (want %s)", module, want)
		}
	}
	if !strings.Contains(body, `<link rel="stylesheet" href="app.css">`) {
		t.Error("index.html does not link app.css")
	}
}

// TestLocalReferencesResolve catches a typo in a module or font path, which
// otherwise shows up only as a blank screen on a device with no console.
func TestLocalReferencesResolve(t *testing.T) {
	ref := regexp.MustCompile(`(?:src|href)="([^"]+)"`)
	cssURL := regexp.MustCompile(`url\(["']?([^"')]+)["']?\)`)
	root := Static()

	check := func(from, target string) {
		// Fragments and server routes are not embedded assets.
		if target == "" || strings.HasPrefix(target, "#") || strings.HasPrefix(target, "/") ||
			strings.HasPrefix(target, "data:") {
			return
		}
		resolved := path.Join(path.Dir(from), target)
		if _, err := fs.Stat(root, resolved); err != nil {
			t.Errorf("%s references %q which is not in the embedded tree (looked for %s)", from, target, resolved)
		}
	}

	walkTextAssets(t, func(name, body string) {
		switch strings.ToLower(path.Ext(name)) {
		case ".html":
			for _, m := range ref.FindAllStringSubmatch(body, -1) {
				check(name, m[1])
			}
		case ".css":
			for _, m := range cssURL.FindAllStringSubmatch(body, -1) {
				check(name, m[1])
			}
		}
	})
}

func excerpt(s string, at int) string {
	end := at + 48
	if end > len(s) {
		end = len(s)
	}
	return strings.TrimSpace(s[at:end])
}
