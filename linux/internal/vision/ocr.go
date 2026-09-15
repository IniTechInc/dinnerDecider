package vision

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// OCR modes, as surfaced by /api/status.
//
// The user is told which one they are in because it changes what the scan can
// see: image + OCR text beats image-only on packaged goods, which is the
// "look-alike products" failure mode OCRService.swift exists for. A box of one
// cereal and a box of another are the same picture until you read the front.
// Silently running without it would make a measurable recall drop look like
// the model being bad at its job.
const (
	OCRModeNone      = "none"
	OCRModeTesseract = "tesseract"
)

// tesseractBinary is the executable looked up on PATH.
const tesseractBinary = "tesseract"

// DefaultOCRTimeout bounds one recognition call.
//
// Not in the Swift, which called a framework in-process. Here OCR is a child
// process, and a wedged child would hold up a scan that is already minutes long
// on a Pi. Exceeding it is just another swallowed failure.
const DefaultOCRTimeout = 20 * time.Second

// ocrWaitDelay bounds how long Wait will keep reading after the child is killed.
//
// Killing the process is NOT enough on its own. exec gives a non-*os.File
// Stdout its own pipe and a copying goroutine, and Wait blocks until that pipe
// reports EOF — which needs every holder of the write end to be gone, not just
// the process we signalled. A tesseract that forked, or a wrapper script whose
// own child outlives it, therefore hangs Wait long past the timeout. Observed,
// not theorised: without this the timeout test ran the full 30 seconds.
const ocrWaitDelay = 2 * time.Second

// OCR reads packaging text off an image so it can be fused into the prompt.
//
// Port of `OCRService`, Stage 3 of shared/pipeline/scan-pipeline.md. Run once
// over the whole normalised photo for Pass A and once per crop for Pass B,
// immediately before each image is sent to the model.
//
// RecognizeText NEVER returns an error and never fails a scan: an OCR problem
// yields an empty string and the scan continues image-only. At the prompt
// boundary an empty result is trimmed and substituted with the literal `none`.
//
// Mode is not in the Swift's shape and is here because the Linux port has a
// choice iOS did not: tesseract may simply not be installed. /api/status
// surfaces this, so an operator can see why recall is lower than they expected
// instead of guessing.
type OCR interface {
	RecognizeText(img image.Image) string
	Mode() string
}

// NoopOCR is the implementation used when tesseract is not installed.
//
// Returning "" is a fully supported state, not a degraded one — the prompt
// boundary already substitutes `none` and the scan runs image-only.
type NoopOCR struct{}

// RecognizeText always returns the empty string.
func (NoopOCR) RecognizeText(image.Image) string { return "" }

// Mode returns OCRModeNone.
func (NoopOCR) Mode() string { return OCRModeNone }

// TesseractOCR shells out to the tesseract binary.
//
// An exec, never a link: the Go bindings (gosseract) need cgo, and
// CGO_ENABLED=0 is a hard build gate for this port. Exec also means a crash or
// a hang in tesseract costs one empty string rather than the whole service.
type TesseractOCR struct {
	// Path is the tesseract executable. An empty Path makes every call a
	// no-op, so a zero TesseractOCR fails closed rather than panicking.
	Path string

	// Timeout bounds one call. Zero means DefaultOCRTimeout.
	Timeout time.Duration
}

// Mode returns OCRModeTesseract.
func (TesseractOCR) Mode() string { return OCRModeTesseract }

// RecognizeText runs `tesseract stdin stdout --psm 6` over a PNG of img and
// returns the recognised lines joined by a SINGLE SPACE.
//
// No newlines and no confidence filtering, matching the Swift, which takes the
// top candidate of each recognised line and joins with " ". The prompt is a
// single line — see shared/prompts — so a newline here would break its shape.
//
// --psm 6 is "assume a single uniform block of text", which is the right
// assumption for a crop of one product's packaging.
//
// Every failure — encode, spawn, non-zero exit, timeout, or a child that
// outlived its kill — becomes "".
func (t TesseractOCR) RecognizeText(img image.Image) string {
	if img == nil || t.Path == "" {
		return ""
	}

	var input bytes.Buffer
	if err := png.Encode(&input, img); err != nil {
		return ""
	}

	timeout := t.Timeout
	if timeout <= 0 {
		timeout = DefaultOCRTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var output bytes.Buffer
	cmd := exec.CommandContext(ctx, t.Path, "stdin", "stdout", "--psm", "6")
	cmd.Stdin = &input
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	cmd.WaitDelay = ocrWaitDelay
	if err := cmd.Run(); err != nil {
		return ""
	}
	return joinRecognizedLines(output.String())
}

// joinRecognizedLines collapses tesseract's line-per-row output into the one
// flat string the prompt expects.
//
// Blank lines are dropped rather than joined, so the result never carries a
// double space, and tesseract's trailing form feed goes with them.
func joinRecognizedLines(raw string) string {
	var lines []string
	for _, line := range strings.Split(raw, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return strings.Join(lines, " ")
}

var ocrLogOnce sync.Once

// DetectOCR picks an implementation by looking for tesseract on PATH, and logs
// the choice once.
//
// Call it at startup and keep the result: the answer does not change while the
// service runs, and the log line is the operator's one chance to notice they
// are scanning without OCR before they blame the model.
func DetectOCR() OCR {
	path, err := exec.LookPath(tesseractBinary)
	if err != nil {
		ocrLogOnce.Do(func() {
			log.Printf("[vision] OCR mode=%s: %q not found on PATH; scans will run image-only, "+
				"which measurably lowers recall on packaged goods", OCRModeNone, tesseractBinary)
		})
		return NoopOCR{}
	}
	ocrLogOnce.Do(func() {
		log.Printf("[vision] OCR mode=%s: using %s", OCRModeTesseract, path)
	})
	return TesseractOCR{Path: path}
}
