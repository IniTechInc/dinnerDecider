package api

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// pngHeaderOnly builds a PNG that is a valid signature plus a valid IHDR chunk
// declaring w x h, and NOTHING else: no pixel data at all.
//
// This is the decode bomb in its purest form. The file is 45 bytes. A decoder
// asked to decode it would allocate w*h*4 bytes before discovering there is no
// image here, which for 40000x40000 is 6.4 GB on a box with 8.
func pngHeaderOnly(w, h uint32) []byte {
	var buf bytes.Buffer
	buf.WriteString("\x89PNG\r\n\x1a\n")

	var ihdr bytes.Buffer
	ihdr.WriteString("IHDR")
	_ = binary.Write(&ihdr, binary.BigEndian, w)
	_ = binary.Write(&ihdr, binary.BigEndian, h)
	ihdr.WriteByte(8) // bit depth
	ihdr.WriteByte(2) // colour type: truecolour
	ihdr.WriteByte(0) // compression
	ihdr.WriteByte(0) // filter
	ihdr.WriteByte(0) // interlace

	payload := ihdr.Bytes()
	_ = binary.Write(&buf, binary.BigEndian, uint32(len(payload)-4))
	buf.Write(payload)
	_ = binary.Write(&buf, binary.BigEndian, crc32.ChecksumIEEE(payload))
	return buf.Bytes()
}

// tinyPNG is a real, decodable 4x4 image.
func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for x := range 4 {
		for y := range 4 {
			img.Set(x, y, color.RGBA{R: uint8(x * 60), G: uint8(y * 60), B: 10, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding the test png: %v", err)
	}
	return buf.Bytes()
}

// multipartBody builds a photos upload out of the given parts.
func multipartBody(t *testing.T, parts map[string][]byte) (string, []byte) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	names := make([]string, 0, len(parts))
	for name := range parts {
		names = append(names, name)
	}
	// Stable order so the test is not at the mercy of map iteration.
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if names[j] < names[i] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	for _, name := range names {
		w, err := mw.CreateFormFile("photos", name)
		if err != nil {
			t.Fatalf("creating the form file: %v", err)
		}
		if _, err := w.Write(parts[name]); err != nil {
			t.Fatalf("writing the form file: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("closing the multipart writer: %v", err)
	}
	return mw.FormDataContentType(), buf.Bytes()
}

func uploadRequest(t *testing.T, contentType string, body []byte) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/photos", bytes.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	return httptest.NewRecorder(), r
}

func asLimitError(t *testing.T, err error) *LimitError {
	t.Helper()
	if err == nil {
		t.Fatal("expected a refusal, got nil")
	}
	var limitErr *LimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("expected a *LimitError, got %T: %v", err, err)
	}
	return limitErr
}

// THE decode bomb test. The assertion that matters is not the status code, it
// is decodeCalled staying false: the refusal must come from the header check,
// with the decoder never reached.
func TestDecodeBombIsRejectedWithoutDecoding(t *testing.T) {
	var mu sync.Mutex
	decodeCalled := false

	limits := DefaultLimits()
	limits.decodeImage = func([]byte) (image.Image, error) {
		mu.Lock()
		decodeCalled = true
		mu.Unlock()
		t.Error("the decoder was reached: a 40000x40000 image must be refused from its header alone")
		return nil, errors.New("must not be called")
	}

	bomb := pngHeaderOnly(40000, 40000)
	if len(bomb) > 100 {
		t.Fatalf("the bomb should be a header and nothing else, got %d bytes", len(bomb))
	}

	_, err := limits.DecodeUpload(bomb)
	limitErr := asLimitError(t, err)

	mu.Lock()
	called := decodeCalled
	mu.Unlock()
	if called {
		t.Fatal("image.Decode was called on a 40000x40000 header")
	}
	if limitErr.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", limitErr.Status)
	}
	if limitErr.Code != CodeTooManyPixels {
		t.Fatalf("code = %q, want %q", limitErr.Code, CodeTooManyPixels)
	}
	if !errors.Is(err, ErrTooManyPixels) {
		t.Fatal("the refusal should match ErrTooManyPixels")
	}
	if !strings.Contains(limitErr.Message, "40000x40000") {
		t.Fatalf("the message should name the real size, got %q", limitErr.Message)
	}
}

// A long thin strip can slip under the pixel product while still being absurd,
// so either side is capped on its own.
func TestSingleDimensionCapIsCheckedFromTheHeader(t *testing.T) {
	limits := DefaultLimits()
	limits.decodeImage = func([]byte) (image.Image, error) {
		t.Error("the decoder was reached for an over-wide image")
		return nil, errors.New("must not be called")
	}
	// 30000 x 100 is 3 megapixels, well under the 40 megapixel cap.
	_, err := limits.DecodeUpload(pngHeaderOnly(30000, 100))
	limitErr := asLimitError(t, err)
	if limitErr.Code != CodeTooManyPixels {
		t.Fatalf("code = %q, want %q", limitErr.Code, CodeTooManyPixels)
	}
	if !strings.Contains(limitErr.Message, "20000") {
		t.Fatalf("the message should name the per-side limit, got %q", limitErr.Message)
	}
}

// An oversize BODY is refused naming the TOTAL limit, because that is the limit
// that bound. Sending fewer photos is the fix, and the message has to say so.
func TestOversizeBodyIsRefusedNamingTheTotalLimit(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxRequestBytes = 4 << 20 // 4 MB, so the test does not move 49 MB

	payload := bytes.Repeat([]byte{0x41}, int(limits.MaxRequestBytes)+(1<<20))
	contentType, body := multipartBody(t, map[string][]byte{"big.png": payload})
	w, r := uploadRequest(t, contentType, body)

	_, err := limits.ReadUploads(w, r)
	limitErr := asLimitError(t, err)

	if limitErr.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", limitErr.Status)
	}
	if limitErr.Code != CodeRequestTooLarge {
		t.Fatalf("code = %q, want %q", limitErr.Code, CodeRequestTooLarge)
	}
	if !strings.Contains(limitErr.Message, "4 MB total limit") {
		t.Fatalf("the message must name the TOTAL limit, got %q", limitErr.Message)
	}
	if strings.Contains(limitErr.Message, "per-photo") {
		t.Fatalf("the message names the wrong limit: %q", limitErr.Message)
	}
}

// The shipped default reads the way the operator expects, with no test-sized
// numbers in it.
func TestDefaultTotalLimitMessageNamesFortyEightMegabytes(t *testing.T) {
	msg := DefaultLimits().totalTooLarge().Message
	if !strings.Contains(msg, "48 MB total limit") {
		t.Fatalf("default total message = %q", msg)
	}
}

// A single 20 MB photo fits inside the 48 MB total allowance, so the limit that
// bound is the PER-FILE one and the message must say that instead. Sending
// fewer photos would not help here, and telling the user to is a dead end.
func TestSingleOversizeFileIsRefusedNamingThePerFileLimit(t *testing.T) {
	limits := DefaultLimits()

	payload := bytes.Repeat([]byte{0x41}, 20<<20)
	if int64(len(payload)) >= limits.MaxRequestBytes {
		t.Fatalf("the fixture must fit inside the total allowance: %d >= %d", len(payload), limits.MaxRequestBytes)
	}
	contentType, body := multipartBody(t, map[string][]byte{"huge.jpg": payload})
	w, r := uploadRequest(t, contentType, body)

	_, err := limits.ReadUploads(w, r)
	limitErr := asLimitError(t, err)

	if limitErr.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", limitErr.Status)
	}
	if limitErr.Code != CodeFileTooLarge {
		t.Fatalf("code = %q, want %q", limitErr.Code, CodeFileTooLarge)
	}
	if !strings.Contains(limitErr.Message, "12 MB per-photo limit") {
		t.Fatalf("the message must name the PER-FILE limit, got %q", limitErr.Message)
	}
	if strings.Contains(limitErr.Message, "total limit") {
		t.Fatalf("the message names the wrong limit: %q", limitErr.Message)
	}
	if !strings.Contains(limitErr.Message, "huge.jpg") {
		t.Fatalf("the message should name the photo, got %q", limitErr.Message)
	}
}

func TestTooManyFilesIsRefusedBeforeAnythingIsRead(t *testing.T) {
	limits := DefaultLimits()
	parts := map[string][]byte{}
	for i := range limits.MaxFilesPerRequest + 1 {
		parts[string(rune('a'+i))+".png"] = tinyPNG(t)
	}
	contentType, body := multipartBody(t, parts)
	w, r := uploadRequest(t, contentType, body)

	_, err := limits.ReadUploads(w, r)
	limitErr := asLimitError(t, err)
	if limitErr.Code != CodeTooManyFiles {
		t.Fatalf("code = %q, want %q", limitErr.Code, CodeTooManyFiles)
	}
	if !strings.Contains(limitErr.Message, "8 is the most") {
		t.Fatalf("the message should name the file-count limit, got %q", limitErr.Message)
	}
}

// The format is sniffed, so naming a GIF "photo.jpg" changes nothing.
func TestFormatIsSniffedNotTakenFromTheName(t *testing.T) {
	limits := DefaultLimits()
	gif := append([]byte("GIF89a"), bytes.Repeat([]byte{0}, 32)...)
	contentType, body := multipartBody(t, map[string][]byte{"photo.jpg": gif})
	w, r := uploadRequest(t, contentType, body)

	_, err := limits.ReadUploads(w, r)
	limitErr := asLimitError(t, err)
	if limitErr.Status != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", limitErr.Status)
	}
	if limitErr.Code != CodeUnsupportedFormat {
		t.Fatalf("code = %q, want %q", limitErr.Code, CodeUnsupportedFormat)
	}
}

// HEIC gets its own message: it is what a modern phone camera produces by
// default, and "unsupported format" would be a dead end where the real answer
// is that the web app converts it in the browser.
func TestHEICGetsAnActionableMessage(t *testing.T) {
	heic := make([]byte, 32)
	copy(heic[4:], "ftypheic")
	_, err := DefaultLimits().DecodeUpload(heic)
	limitErr := asLimitError(t, err)
	if !strings.Contains(limitErr.Message, "HEIC") {
		t.Fatalf("message = %q", limitErr.Message)
	}
}

func TestGoodUploadIsAcceptedAndDecoded(t *testing.T) {
	contentType, body := multipartBody(t, map[string][]byte{"a.png": tinyPNG(t), "b.png": tinyPNG(t)})
	w, r := uploadRequest(t, contentType, body)

	uploads, err := DefaultLimits().ReadUploads(w, r)
	if err != nil {
		t.Fatalf("a good upload was refused: %v", err)
	}
	if len(uploads) != 2 {
		t.Fatalf("got %d uploads, want 2", len(uploads))
	}
	for _, upload := range uploads {
		if upload.Image == nil {
			t.Fatal("an accepted upload must carry decoded pixels")
		}
		if upload.Width != 4 || upload.Height != 4 {
			t.Fatalf("size = %dx%d, want 4x4", upload.Width, upload.Height)
		}
		if upload.MimeType != "image/png" {
			t.Fatalf("mime = %q, want image/png", upload.MimeType)
		}
	}
	if uploads[0].FileName != "a.png" || uploads[1].FileName != "b.png" {
		t.Fatalf("parts are not in a deterministic order: %q, %q", uploads[0].FileName, uploads[1].FileName)
	}
}

func TestNonMultipartUploadIsRefused(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/photos", bytes.NewReader(tinyPNG(t)))
	r.Header.Set("Content-Type", "image/png")
	_, err := DefaultLimits().ReadUploads(httptest.NewRecorder(), r)
	limitErr := asLimitError(t, err)
	if limitErr.Status != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", limitErr.Status)
	}
}

func TestSafeFileNameKeepsNothingDangerous(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd":                "passwd",
		`C:\Users\x\a.png`:                "a.png",
		"ok.png":                          "ok.png",
		"we\x00ird\x07.png":               "weird.png",
		strings.Repeat("a", 200) + ".png": strings.Repeat("a", 64),
	}
	for in, want := range cases {
		if got := safeFileName(in); got != want {
			t.Errorf("safeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSSEGateBoundsTheServiceTotal(t *testing.T) {
	gate := newSSEGate(3)
	for i := range 3 {
		if !gate.acquire() {
			t.Fatalf("subscriber %d was refused below the cap", i+1)
		}
	}
	if gate.acquire() {
		t.Fatal("the 4th subscriber was admitted past a cap of 3")
	}
	if gate.count() != 3 {
		t.Fatalf("count = %d, want 3", gate.count())
	}
	gate.release()
	if !gate.acquire() {
		t.Fatal("a released slot was not reusable")
	}
}

func TestSSEGateIsSafeUnderConcurrency(t *testing.T) {
	gate := newSSEGate(8)
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if gate.acquire() {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if admitted != 8 {
		t.Fatalf("admitted %d, want exactly 8", admitted)
	}
}

func TestLimitsWithDefaultsFillsOnlyTheZeroes(t *testing.T) {
	l := Limits{MaxFileBytes: 7}.withDefaults()
	if l.MaxFileBytes != 7 {
		t.Fatalf("an explicit limit was overwritten: %d", l.MaxFileBytes)
	}
	if l.MaxRequestBytes != MaxRequestBytes {
		t.Fatalf("MaxRequestBytes = %d, want the default", l.MaxRequestBytes)
	}
	if l.decodeConfig == nil || l.decodeImage == nil {
		t.Fatal("the decode hooks must always be filled")
	}
}
