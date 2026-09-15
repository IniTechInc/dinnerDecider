package vision

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeTesseract writes a shell script named "tesseract" into a fresh directory
// and puts that directory first on PATH. body is the script's payload, after
// the shebang.
func fakeTesseract(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake tesseract is a POSIX shell script")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, tesseractBinary)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("writing the fake tesseract: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return path
}

func ocrProbeImage() image.Image {
	return solidNRGBA(24, 16, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
}

func TestNoopOCRReturnsEmptyAndReportsItsMode(t *testing.T) {
	var ocr OCR = NoopOCR{}
	if got := ocr.RecognizeText(ocrProbeImage()); got != "" {
		t.Errorf("RecognizeText = %q, want \"\"", got)
	}
	if got := ocr.RecognizeText(nil); got != "" {
		t.Errorf("RecognizeText(nil) = %q, want \"\"", got)
	}
	if got := ocr.Mode(); got != OCRModeNone {
		t.Errorf("Mode = %q, want %q", got, OCRModeNone)
	}
}

// A single space, no newlines, nothing dropped for confidence. The prompt is
// one line, so a newline here would break its shape.
func TestJoinRecognizedLines(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"single line", "ORGANIC WHOLE MILK\n", "ORGANIC WHOLE MILK"},
		{"several lines join with one space", "HORIZON\nORGANIC\nWHOLE MILK\n", "HORIZON ORGANIC WHOLE MILK"},
		{"blank lines never become double spaces", "HORIZON\n\n\nWHOLE MILK\n", "HORIZON WHOLE MILK"},
		{"carriage returns are trimmed", "HORIZON\r\nWHOLE MILK\r\n", "HORIZON WHOLE MILK"},
		{"the trailing form feed goes too", "HORIZON\nWHOLE MILK\n\f", "HORIZON WHOLE MILK"},
		{"leading and trailing spaces are trimmed per line", "   HORIZON  \n  WHOLE MILK \n", "HORIZON WHOLE MILK"},
		{"nothing recognised", "\n\n\f", ""},
		{"empty output", "", ""},
		{"inner spacing is left alone", "1/2 GAL  64 FL OZ\n", "1/2 GAL  64 FL OZ"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := joinRecognizedLines(c.raw)
			if got != c.want {
				t.Fatalf("joinRecognizedLines(%q) = %q, want %q", c.raw, got, c.want)
			}
			if strings.ContainsAny(got, "\n\r\f") {
				t.Fatalf("result %q still contains a line break", got)
			}
		})
	}
}

func TestTesseractOCRInvocationAndOutput(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	stdinFile := filepath.Join(t.TempDir(), "stdin")
	t.Setenv("DD_TEST_ARGS", argsFile)
	t.Setenv("DD_TEST_STDIN", stdinFile)

	path := fakeTesseract(t, `
printf '%s\n' "$@" > "$DD_TEST_ARGS"
cat > "$DD_TEST_STDIN"
printf 'HORIZON\r\n\nORGANIC WHOLE MILK\n\f'
`)

	ocr := TesseractOCR{Path: path}
	got := ocr.RecognizeText(ocrProbeImage())

	if want := "HORIZON ORGANIC WHOLE MILK"; got != want {
		t.Fatalf("RecognizeText = %q, want %q", got, want)
	}
	if mode := ocr.Mode(); mode != OCRModeTesseract {
		t.Errorf("Mode = %q, want %q", mode, OCRModeTesseract)
	}

	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("reading the recorded args: %v", err)
	}
	if want := "stdin\nstdout\n--psm\n6\n"; string(args) != want {
		t.Errorf("args = %q, want %q", args, want)
	}

	stdin, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("reading the recorded stdin: %v", err)
	}
	if len(stdin) < 8 || string(stdin[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Errorf("stdin was not a PNG (first bytes %q)", stdin[:min(8, len(stdin))])
	}
}

// Every failure mode becomes "". The scan continues image-only and the prompt
// boundary substitutes the literal `none`; none of this is an error the user
// ever sees.
func TestTesseractOCRSwallowsEveryFailure(t *testing.T) {
	t.Run("non-zero exit", func(t *testing.T) {
		path := fakeTesseract(t, "printf 'PARTIAL OUTPUT\\n'\nexit 1\n")
		if got := (TesseractOCR{Path: path}).RecognizeText(ocrProbeImage()); got != "" {
			t.Fatalf("RecognizeText = %q, want \"\"", got)
		}
	})
	t.Run("binary missing", func(t *testing.T) {
		ocr := TesseractOCR{Path: filepath.Join(t.TempDir(), "not-installed")}
		if got := ocr.RecognizeText(ocrProbeImage()); got != "" {
			t.Fatalf("RecognizeText = %q, want \"\"", got)
		}
	})
	t.Run("zero value fails closed", func(t *testing.T) {
		if got := (TesseractOCR{}).RecognizeText(ocrProbeImage()); got != "" {
			t.Fatalf("RecognizeText = %q, want \"\"", got)
		}
	})
	t.Run("nil image", func(t *testing.T) {
		path := fakeTesseract(t, "printf 'SHOULD NOT RUN\\n'\n")
		if got := (TesseractOCR{Path: path}).RecognizeText(nil); got != "" {
			t.Fatalf("RecognizeText(nil) = %q, want \"\"", got)
		}
	})
	t.Run("recognises nothing", func(t *testing.T) {
		path := fakeTesseract(t, "cat > /dev/null\nprintf '\\n\\f'\n")
		if got := (TesseractOCR{Path: path}).RecognizeText(ocrProbeImage()); got != "" {
			t.Fatalf("RecognizeText = %q, want \"\"", got)
		}
	})
}

// A wedged child must not hold up a scan that is already minutes long.
//
// The script sleeps in a CHILD of the shell, deliberately: that child inherits
// the stdout pipe, so killing the shell on timeout does not close it and Wait
// would block for the full 30 seconds without cmd.WaitDelay. Expected wall
// time is the 250 ms timeout plus ocrWaitDelay.
func TestTesseractOCRTimesOutRatherThanHanging(t *testing.T) {
	path := fakeTesseract(t, "cat > /dev/null\nsleep 30\nprintf 'TOO LATE\\n'\n")

	start := time.Now()
	got := (TesseractOCR{Path: path, Timeout: 250 * time.Millisecond}).RecognizeText(ocrProbeImage())
	elapsed := time.Since(start)

	if got != "" {
		t.Fatalf("RecognizeText = %q, want \"\"", got)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("RecognizeText took %v; the timeout did not fire", elapsed)
	}
}

func TestDetectOCRPicksTesseractWhenItIsOnPath(t *testing.T) {
	path := fakeTesseract(t, "cat > /dev/null\nprintf 'CHEERIOS\\n'\n")

	ocr := DetectOCR()
	if got := ocr.Mode(); got != OCRModeTesseract {
		t.Fatalf("Mode = %q, want %q", got, OCRModeTesseract)
	}
	tess, ok := ocr.(TesseractOCR)
	if !ok {
		t.Fatalf("DetectOCR returned %T, want TesseractOCR", ocr)
	}
	if tess.Path != path {
		t.Errorf("Path = %q, want %q", tess.Path, path)
	}
	if got := ocr.RecognizeText(ocrProbeImage()); got != "CHEERIOS" {
		t.Errorf("RecognizeText = %q, want \"CHEERIOS\"", got)
	}
}

func TestDetectOCRFallsBackToNoopWhenTesseractIsAbsent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH manipulation differs on Windows")
	}
	t.Setenv("PATH", t.TempDir())

	ocr := DetectOCR()
	if _, ok := ocr.(NoopOCR); !ok {
		t.Fatalf("DetectOCR returned %T, want NoopOCR", ocr)
	}
	if got := ocr.Mode(); got != OCRModeNone {
		t.Errorf("Mode = %q, want %q", got, OCRModeNone)
	}
}
