package web

import (
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func readAsset(t *testing.T, name string) string {
	t.Helper()
	raw, err := fs.ReadFile(Static(), name)
	if err != nil {
		t.Fatalf("reading %s from the embedded tree: %v", name, err)
	}
	return string(raw)
}

// TestFontsAreLocalAndLicensed pins the two halves of shipping an OFL face:
// the file itself, and the licence that permits shipping it.
func TestFontsAreLocalAndLicensed(t *testing.T) {
	for _, name := range []string{
		"fonts/DMSerifDisplay-Regular.ttf",
		"fonts/DMSans-Regular.ttf",
		"fonts/DMSans-Medium.ttf",
		"fonts/DMSans-SemiBold.ttf",
		"fonts/DMSans-Bold.ttf",
		"fonts/OFL-DMSans.txt",
		"fonts/OFL-DMSerifDisplay.txt",
	} {
		info, err := fs.Stat(Static(), name)
		if err != nil {
			t.Errorf("%s is not embedded: %v", name, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("%s is embedded but empty", name)
		}
	}
}

// serifFloorRem is the title2 scale: 22px. DM Serif Display ships a single
// weight and reads poorly below it, so the stylesheet is not allowed to use the
// display family at any smaller size. This is the check that keeps a later
// "just make this label fancy" edit honest.
const serifFloorRem = 1.375

var (
	fontSizeDecl = regexp.MustCompile(`font-size:\s*([^;]+);`)
	remValue     = regexp.MustCompile(`([0-9]*\.?[0-9]+)rem`)
)

func TestDisplayFaceNeverBelowTitle2(t *testing.T) {
	css := readAsset(t, "app.css")

	// Crude but sufficient: CSS rule bodies do not nest, so splitting on the
	// closing brace yields one chunk per rule (plus at-rule preludes, which
	// carry no font-family declaration of their own).
	for _, block := range strings.Split(css, "}") {
		if !strings.Contains(block, "--font-display") {
			continue
		}
		if strings.Contains(block, ":root") {
			continue // the token definition itself
		}
		selector := strings.TrimSpace(block)
		if i := strings.Index(selector, "{"); i >= 0 {
			selector = strings.TrimSpace(selector[:i])
		}

		m := fontSizeDecl.FindStringSubmatch(block)
		if m == nil {
			t.Errorf("%q uses the display face but sets no font-size, so the 22px floor cannot be verified", selector)
			continue
		}
		value := m[1]

		// clamp(min, preferred, max): the min is what a 320px phone gets, so it
		// is the number the floor applies to.
		sizes := remValue.FindAllStringSubmatch(value, -1)
		if len(sizes) == 0 {
			t.Errorf("%q sets font-size %q in units this test cannot check; express display sizes in rem", selector, strings.TrimSpace(value))
			continue
		}
		smallest := 1e9
		if strings.Contains(value, "clamp(") {
			f, err := strconv.ParseFloat(sizes[0][1], 64)
			if err != nil {
				t.Errorf("%q: cannot parse the clamp minimum %q", selector, sizes[0][1])
				continue
			}
			smallest = f
		} else {
			for _, s := range sizes {
				f, err := strconv.ParseFloat(s[1], 64)
				if err != nil {
					continue
				}
				if f < smallest {
					smallest = f
				}
			}
		}
		if smallest < serifFloorRem {
			t.Errorf("%q renders DM Serif Display at %grem, below the %grem (title2) floor", selector, smallest, serifFloorRem)
		}
	}
}

// TestResponsiveContract pins the breakpoints and the safe-area handling that
// "any phone on the LAN becomes the front end" actually depends on.
func TestResponsiveContract(t *testing.T) {
	css := readAsset(t, "app.css")

	for _, want := range []string{
		"@media (min-width: 641px)",  // nav becomes a rail, two columns
		"@media (min-width: 1025px)", // centred
		"max-width: 1100px",
		"env(safe-area-inset-top",
		"env(safe-area-inset-right",
		"env(safe-area-inset-bottom",
		"env(safe-area-inset-left",
		"--touch: 44px",
		"@media (prefers-color-scheme: dark)",
		"@media (prefers-reduced-motion: reduce)",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css is missing %q", want)
		}
	}

	// Mobile-first: the base rules are the <=640px case, so there must be no
	// max-width media query undoing them.
	if strings.Contains(css, "@media (max-width") {
		t.Error("app.css uses a max-width media query; the base rules are meant to BE the phone layout")
	}
}

// TestPaletteTokens keeps the stylesheet honest against CLAUDE.md.
func TestPaletteTokens(t *testing.T) {
	css := readAsset(t, "app.css")
	for name, hex := range map[string]string{
		"terracotta": "#E4573D",
		"sage":       "#7FA27A",
		"amber":      "#F2B23C",
		"cream":      "#FFF6E8",
		"charcoal":   "#1F1D1B",
		"greige":     "#CFC7BD",
	} {
		if !strings.Contains(css, hex) {
			t.Errorf("app.css never uses the %s token %s", name, hex)
		}
	}
}
