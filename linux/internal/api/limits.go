// Package api is the service's HTTP surface: the route table, the boundary
// limits every request passes through, and the handlers behind them.
package api

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"io"
	"mime/multipart"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	_ "image/jpeg" // registers the JPEG decoder with image.DecodeConfig
	_ "image/png"  // registers the PNG decoder with image.DecodeConfig

	_ "golang.org/x/image/webp" // registers the WebP decoder

	"github.com/IniTechInc/dinnerDecider/linux/internal/vision"
)

// The boundary limits.
//
// WHY THESE EXIST AT ALL. The whole architecture of this port is built around a
// 4 to 5 GiB working set on an 8 GiB box, with llama-server holding most of it
// wired. In that budget a single crafted PNG, a single slow connection, or an
// unbounded registry does not degrade the service: it OOMs the machine and the
// kernel reaps llama-server, which looks to the user like the scan failing for
// no reason. LAN exposure is a first-class, supported posture here, so every
// one of these is reachable by anything on the Wi-Fi rather than theoretical.
//
// Every limit below is also a config key (internal/config), so an operator on
// bigger hardware can raise them. These are the defaults.
const (
	// MaxRequestBytes caps a whole request body and is enforced with
	// http.MaxBytesReader BEFORE any parsing. It binds first, always: a parser
	// that has already buffered 2 GiB has lost, whatever it decides next.
	MaxRequestBytes int64 = 48 << 20
	// MaxFileBytes caps one photo. Eight of these exceed MaxRequestBytes, and
	// that is deliberate: this is not the total, it is defence in depth for the
	// single-file request that fits inside the total but is still absurd.
	MaxFileBytes int64 = 12 << 20
	// MaxFilesPerRequest caps how many photos one upload may carry. The scan
	// batch itself is bounded by the call budget, not by this.
	MaxFilesPerRequest = 8
	// MaxUploadPixels caps width*height. Checked from the image HEADER via
	// image.DecodeConfig, before any decode, so a 64000x64000 decode bomb is
	// refused having allocated nothing but the header read.
	MaxUploadPixels int64 = 40_000_000
	// MaxUploadDimension caps either side on its own, so a 1x80000000 strip
	// that squeaks under the pixel product is still refused.
	MaxUploadDimension = 20000
	// MaxMultipartMemory is what multipart parsing keeps in RAM; the rest
	// spills to a temp file that the parser removes.
	MaxMultipartMemory int64 = 8 << 20

	// MaxConcurrentScans is the scan registry cap: one running scan plus one
	// finished but unconfirmed. Beyond it the request is refused with 429 and
	// the oldest TERMINAL scan's photos are swept, rather than queued behind a
	// four-minute run the client cannot see.
	MaxConcurrentScans = 2
	// MaxSSEPerScan caps event-stream subscribers on a single stream.
	MaxSSEPerScan = 16
	// MaxSSETotal caps event-stream subscribers service-wide.
	MaxSSETotal = 64
)

// Streaming deadlines.
//
// The SSE routes run with NO write deadline, which is correct for a stream and
// means nothing in net/http will ever reap an idle one. These two constants are
// the replacement for that: a keepalive comment often enough to keep
// intermediaries from closing the connection, and a hard idle deadline after
// which a stream that has produced no real event is closed by us.
//
// The keepalive deliberately does NOT reset the idle deadline. If it did, the
// deadline would only ever fire on a connection that was already dead, which is
// the one case the operating system handles on its own.
const (
	// SSEIdleDeadline is how long a stream may go without a real event.
	SSEIdleDeadline = 10 * time.Minute
	// SSEKeepalive is the comment-frame interval.
	SSEKeepalive = 20 * time.Second
	// SSERetryAfter is the Retry-After on a refused subscription.
	SSERetryAfter = 10 * time.Second
	// SSEClientRetry is the reconnection delay suggested to EventSource.
	SSEClientRetry = 3 * time.Second
	// ScanRetryAfter is the Retry-After on a refused scan.
	ScanRetryAfter = 30 * time.Second
)

// CaptureJPEGQuality is the quality an accepted upload is re-encoded at.
//
// Uploads are normalised once, at the boundary: EXIF orientation baked in, the
// longest side capped at vision.CaptureMaxSide, re-encoded as JPEG. The stored
// bytes are therefore bounded and already in the shape Stage 0 of
// shared/pipeline/scan-pipeline.md wants, so starting a scan re-decodes a small
// image instead of eight full-resolution ones. 90 matches the quality the model
// input itself is encoded at.
const CaptureJPEGQuality = 90

// Limits is the resolved set of boundary limits for one service.
//
// A value rather than package constants so the config layer can raise them on
// hardware that is not a Raspberry Pi, and so tests can shrink them to
// something a test can actually produce.
type Limits struct {
	MaxRequestBytes    int64
	MaxFileBytes       int64
	MaxFilesPerRequest int
	MaxUploadPixels    int64
	MaxUploadDimension int
	MaxMultipartMemory int64
	MaxConcurrentScans int
	MaxSSEPerScan      int
	MaxSSETotal        int
	SSEIdleDeadline    time.Duration
	SSEKeepalive       time.Duration

	// decodeConfig reads an image HEADER. decodeImage decodes the pixels.
	//
	// They are separate, injectable fields for one reason: the pixel gate's
	// whole value is that it runs on the header and REFUSES before the decoder
	// is ever reached. A test can install a decodeImage that records being
	// called and assert it never was, which turns "we check first" from a
	// comment into an assertion.
	decodeConfig func(data []byte) (image.Config, string, error)
	decodeImage  func(data []byte) (image.Image, error)
}

// DefaultLimits returns the shipped defaults.
func DefaultLimits() Limits {
	return Limits{
		MaxRequestBytes:    MaxRequestBytes,
		MaxFileBytes:       MaxFileBytes,
		MaxFilesPerRequest: MaxFilesPerRequest,
		MaxUploadPixels:    MaxUploadPixels,
		MaxUploadDimension: MaxUploadDimension,
		MaxMultipartMemory: MaxMultipartMemory,
		MaxConcurrentScans: MaxConcurrentScans,
		MaxSSEPerScan:      MaxSSEPerScan,
		MaxSSETotal:        MaxSSETotal,
		SSEIdleDeadline:    SSEIdleDeadline,
		SSEKeepalive:       SSEKeepalive,
	}
}

// withDefaults fills every zero field from DefaultLimits, so a caller may set
// only the one limit it cares about.
func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxRequestBytes <= 0 {
		l.MaxRequestBytes = d.MaxRequestBytes
	}
	if l.MaxFileBytes <= 0 {
		l.MaxFileBytes = d.MaxFileBytes
	}
	if l.MaxFilesPerRequest <= 0 {
		l.MaxFilesPerRequest = d.MaxFilesPerRequest
	}
	if l.MaxUploadPixels <= 0 {
		l.MaxUploadPixels = d.MaxUploadPixels
	}
	if l.MaxUploadDimension <= 0 {
		l.MaxUploadDimension = d.MaxUploadDimension
	}
	if l.MaxMultipartMemory <= 0 {
		l.MaxMultipartMemory = d.MaxMultipartMemory
	}
	if l.MaxConcurrentScans <= 0 {
		l.MaxConcurrentScans = d.MaxConcurrentScans
	}
	if l.MaxSSEPerScan <= 0 {
		l.MaxSSEPerScan = d.MaxSSEPerScan
	}
	if l.MaxSSETotal <= 0 {
		l.MaxSSETotal = d.MaxSSETotal
	}
	if l.SSEIdleDeadline <= 0 {
		l.SSEIdleDeadline = d.SSEIdleDeadline
	}
	if l.SSEKeepalive <= 0 {
		l.SSEKeepalive = d.SSEKeepalive
	}
	if l.decodeConfig == nil {
		l.decodeConfig = defaultDecodeConfig
	}
	if l.decodeImage == nil {
		l.decodeImage = vision.DecodeNormalized
	}
	return l
}

func defaultDecodeConfig(data []byte) (image.Config, string, error) {
	return image.DecodeConfig(bytes.NewReader(data))
}

// Upload is one accepted image: the bytes as sent, and the decoded, orientation
// normalised pixels.
type Upload struct {
	FileName string
	Bytes    []byte
	Image    image.Image
	Width    int
	Height   int
	MimeType string
}

// LimitError is a refusal at the boundary, carrying the status and the
// user-facing message the handler should send.
//
// The message NAMES THE LIMIT THAT WAS ACTUALLY HIT. "Request too large" for a
// single 20 MB photo sent inside a 48 MB allowance sends the user off to split
// a batch that was never the problem, and a generic truncation message sends
// them off to blame their network.
type LimitError struct {
	Status  int
	Code    string
	Message string
}

// Error implements error.
func (e *LimitError) Error() string { return e.Message }

// ErrRequestTooLarge and friends let a caller match a refusal by class.
var (
	// ErrRequestTooLarge is the whole-body cap.
	ErrRequestTooLarge = errors.New("the request body is over the total limit")
	// ErrFileTooLarge is the per-file cap.
	ErrFileTooLarge = errors.New("a file is over the per-file limit")
	// ErrTooManyFiles is the per-request file count cap.
	ErrTooManyFiles = errors.New("too many files in one request")
	// ErrTooManyPixels is the header-checked pixel cap.
	ErrTooManyPixels = errors.New("the image has too many pixels")
	// ErrUnsupportedUpload is a format this port does not decode.
	ErrUnsupportedUpload = errors.New("unsupported upload format")
)

// Unwrap maps a refusal onto its class error so errors.Is works.
func (e *LimitError) Unwrap() error {
	switch e.Code {
	case CodeRequestTooLarge:
		return ErrRequestTooLarge
	case CodeFileTooLarge:
		return ErrFileTooLarge
	case CodeTooManyFiles:
		return ErrTooManyFiles
	case CodeTooManyPixels:
		return ErrTooManyPixels
	case CodeUnsupportedFormat:
		return ErrUnsupportedUpload
	default:
		return nil
	}
}

// Error codes carried by a LimitError, and by the JSON error envelope.
const (
	CodeRequestTooLarge   = "request_too_large"
	CodeFileTooLarge      = "file_too_large"
	CodeTooManyFiles      = "too_many_files"
	CodeTooManyPixels     = "image_too_large"
	CodeUnsupportedFormat = "unsupported_format"
	CodeBadImage          = "bad_image"
	CodeNoFiles           = "no_files"
	CodeBadContentType    = "bad_content_type"
)

// ReadUploads is the ONE way a photo enters this service.
//
// The order of the checks is the substance of this function and is not
// interchangeable:
//
//  1. http.MaxBytesReader on the raw body, before the multipart parser sees a
//     byte. This is what makes the total cap real rather than advisory.
//  2. the file-count cap, so a request with 4000 tiny parts is refused before
//     any of them is read.
//  3. the per-file cap, from the part header and again while reading.
//  4. sniffed format, never the caller's Content-Type and never the extension.
//  5. image.DecodeConfig, then the pixel gate, and only then the decoder.
func (l Limits) ReadUploads(w http.ResponseWriter, r *http.Request) ([]Upload, error) {
	l = l.withDefaults()

	mediaType := r.Header.Get("Content-Type")
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(mediaType)), "multipart/form-data") {
		return nil, &LimitError{
			Status:  http.StatusUnsupportedMediaType,
			Code:    CodeBadContentType,
			Message: "Send the photos as multipart/form-data.",
		}
	}

	// (1) The total cap binds first, on the raw body.
	r.Body = http.MaxBytesReader(w, r.Body, l.MaxRequestBytes)

	if err := r.ParseMultipartForm(l.MaxMultipartMemory); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, l.totalTooLarge()
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, &LimitError{
				Status:  http.StatusBadRequest,
				Code:    CodeBadImage,
				Message: "The upload ended before it was complete. Try again.",
			}
		}
		return nil, &LimitError{
			Status:  http.StatusBadRequest,
			Code:    CodeBadImage,
			Message: "That upload could not be read as multipart/form-data.",
		}
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	headers := flattenFileHeaders(r.MultipartForm)
	if len(headers) == 0 {
		return nil, &LimitError{
			Status:  http.StatusBadRequest,
			Code:    CodeNoFiles,
			Message: "That request carried no photos.",
		}
	}
	// (2) The count cap, before anything is read.
	if len(headers) > l.MaxFilesPerRequest {
		return nil, &LimitError{
			Status: http.StatusRequestEntityTooLarge,
			Code:   CodeTooManyFiles,
			Message: fmt.Sprintf("That request carries %d photos; %d is the most in one upload.",
				len(headers), l.MaxFilesPerRequest),
		}
	}

	uploads := make([]Upload, 0, len(headers))
	for _, fh := range headers {
		upload, err := l.readOne(fh)
		if err != nil {
			return nil, err
		}
		uploads = append(uploads, upload)
	}
	return uploads, nil
}

// readOne reads and validates a single part.
func (l Limits) readOne(fh *multipart.FileHeader) (Upload, error) {
	name := safeFileName(fh.Filename)

	// (3) The per-file cap, from the declared size first so the refusal is
	// cheap, and then from the read itself so a lying header cannot get past.
	if fh.Size > l.MaxFileBytes {
		return Upload{}, l.fileTooLarge(name)
	}
	f, err := fh.Open()
	if err != nil {
		return Upload{}, &LimitError{
			Status:  http.StatusBadRequest,
			Code:    CodeBadImage,
			Message: "That photo could not be read from the upload.",
		}
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(io.LimitReader(f, l.MaxFileBytes+1))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return Upload{}, l.totalTooLarge()
		}
		return Upload{}, &LimitError{
			Status:  http.StatusBadRequest,
			Code:    CodeBadImage,
			Message: "That photo could not be read from the upload.",
		}
	}
	if int64(len(data)) > l.MaxFileBytes {
		return Upload{}, l.fileTooLarge(name)
	}

	upload, err := l.DecodeUpload(data)
	if err != nil {
		return Upload{}, err
	}
	upload.FileName = name
	return upload, nil
}

// DecodeUpload validates one image's bytes and decodes them.
//
// THE DECODE BOMB. A PNG header is 33 bytes and can honestly declare
// 64000x64000. image.Decode on it allocates 16 GB before it fails, which on
// this hardware is not an error, it is the kernel killing llama-server. So the
// header is read on its own with image.DecodeConfig, the dimensions are checked
// against the caps, and the decoder is reached only by an image that has
// already passed. That ordering is the entire defence.
func (l Limits) DecodeUpload(data []byte) (Upload, error) {
	l = l.withDefaults()

	// (4) Sniffed, never declared. A Content-Type header and a file extension
	// are both just strings the caller chose.
	format := vision.DetectFormat(data)
	mimeType, ok := uploadMimeTypes[format]
	if !ok {
		return Upload{}, &LimitError{
			Status:  http.StatusUnsupportedMediaType,
			Code:    CodeUnsupportedFormat,
			Message: unsupportedMessage(format),
		}
	}

	// (5) Header first.
	cfg, _, err := l.decodeConfig(data)
	if err != nil {
		return Upload{}, &LimitError{
			Status:  http.StatusBadRequest,
			Code:    CodeBadImage,
			Message: "That file looks like an image but its header could not be read.",
		}
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return Upload{}, &LimitError{
			Status:  http.StatusBadRequest,
			Code:    CodeBadImage,
			Message: "That image reports no size.",
		}
	}
	if cfg.Width > l.MaxUploadDimension || cfg.Height > l.MaxUploadDimension {
		return Upload{}, &LimitError{
			Status: http.StatusRequestEntityTooLarge,
			Code:   CodeTooManyPixels,
			Message: fmt.Sprintf("That image is %dx%d; neither side may be over %d pixels.",
				cfg.Width, cfg.Height, l.MaxUploadDimension),
		}
	}
	if int64(cfg.Width)*int64(cfg.Height) > l.MaxUploadPixels {
		return Upload{}, &LimitError{
			Status: http.StatusRequestEntityTooLarge,
			Code:   CodeTooManyPixels,
			Message: fmt.Sprintf("That image is %dx%d, which is over the %d megapixel limit.",
				cfg.Width, cfg.Height, l.MaxUploadPixels/1_000_000),
		}
	}

	// Only now are the pixels touched.
	img, err := l.decodeImage(data)
	if err != nil {
		if errors.Is(err, vision.ErrUnsupportedFormat) {
			return Upload{}, &LimitError{
				Status:  http.StatusUnsupportedMediaType,
				Code:    CodeUnsupportedFormat,
				Message: unsupportedMessage(format),
			}
		}
		return Upload{}, &LimitError{
			Status:  http.StatusBadRequest,
			Code:    CodeBadImage,
			Message: "That image could not be decoded. It may be damaged or only partly uploaded.",
		}
	}
	bounds := img.Bounds()
	return Upload{
		Bytes:    data,
		Image:    img,
		Width:    bounds.Dx(),
		Height:   bounds.Dy(),
		MimeType: mimeType,
	}, nil
}

func (l Limits) totalTooLarge() *LimitError {
	return &LimitError{
		Status: http.StatusRequestEntityTooLarge,
		Code:   CodeRequestTooLarge,
		Message: fmt.Sprintf("That upload is over the %s total limit for one request. Send fewer photos at a time.",
			megabytes(l.MaxRequestBytes)),
	}
}

func (l Limits) fileTooLarge(name string) *LimitError {
	msg := fmt.Sprintf("This photo is over the %s per-photo limit.", megabytes(l.MaxFileBytes))
	if name != "" {
		msg = fmt.Sprintf("This photo is over the %s per-photo limit: %s.", megabytes(l.MaxFileBytes), name)
	}
	return &LimitError{Status: http.StatusRequestEntityTooLarge, Code: CodeFileTooLarge, Message: msg}
}

// uploadMimeTypes are the three formats this port decodes, keyed by the name
// vision.DetectFormat reports.
var uploadMimeTypes = map[string]string{
	"jpeg": "image/jpeg",
	"png":  "image/png",
	"webp": "image/webp",
}

func unsupportedMessage(format string) string {
	switch format {
	case "heic", "avif", "iso-media":
		return "That looks like a HEIC photo. The web app converts HEIC in the browser before uploading, so try again from the app rather than dragging the file in."
	case "":
		return "That file is not a JPEG, PNG or WebP image."
	default:
		return fmt.Sprintf("%s images are not accepted. Send a JPEG, PNG or WebP.", strings.ToUpper(format))
	}
}

// megabytes renders a byte cap the way the message needs to read. The constants
// are binary megabytes; the message says MB, which is what a person expects to
// see next to a photo size.
func megabytes(b int64) string {
	return fmt.Sprintf("%d MB", b/(1<<20))
}

// flattenFileHeaders returns every uploaded part, in a deterministic order: by
// form field name, then by the order the parts arrived within that field.
//
// Deterministic because the order becomes the photo POSITION, which becomes the
// order of the scan batch, and a batch that shuffles between runs is a bug
// nobody can reproduce.
func flattenFileHeaders(form *multipart.Form) []*multipart.FileHeader {
	if form == nil || len(form.File) == 0 {
		return nil
	}
	fields := make([]string, 0, len(form.File))
	for field := range form.File {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	out := make([]*multipart.FileHeader, 0, len(form.File))
	for _, field := range fields {
		for _, fh := range form.File[field] {
			if fh != nil {
				out = append(out, fh)
			}
		}
	}
	return out
}

// safeFileName reduces a caller-supplied filename to something safe to put in
// an error message: the base name only, printable characters only, bounded.
//
// It is never used to build a path. The store names files by UUID precisely so
// that a caller-supplied string never reaches the filesystem.
func safeFileName(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, r := range name {
		if b.Len() >= 64 {
			break
		}
		if unicode.IsPrint(r) && r != '"' && r != '\\' {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// sseGate bounds event-stream subscribers service-wide.
//
// Necessary because the SSE routes run with no write deadline: net/http will
// never reap one, so the only bound on how many a client may open is this.
// Sixty-four idle streams is not a lot of memory on its own; sixty-four
// goroutines each holding a scan snapshot on a box at its memory floor is.
type sseGate struct {
	mu    sync.Mutex
	max   int
	total int
}

func newSSEGate(max int) *sseGate {
	if max <= 0 {
		max = MaxSSETotal
	}
	return &sseGate{max: max}
}

// acquire takes a global slot, reporting false when the service is full.
func (g *sseGate) acquire() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.total >= g.max {
		return false
	}
	g.total++
	return true
}

// release returns a slot. Safe to call once per successful acquire.
func (g *sseGate) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.total > 0 {
		g.total--
	}
}

// count is the live subscriber total.
func (g *sseGate) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.total
}

// base is the state every handler group shares.
type base struct {
	limits Limits
	sse    *sseGate
	logf   func(format string, args ...any)
	now    func() time.Time
}

// Timeouts are the http.Server deadlines.
//
// Go's zero values here are UNLIMITED, which on a LAN-exposed box is an
// invitation: a client that opens a connection and dribbles one header byte a
// minute holds a server goroutine and an fd forever, and a few hundred of them
// hold the whole service. These are set explicitly in router.go for that
// reason, and never left at the default.
//
// Write is the awkward one. It covers handler execution as well as the write
// itself, so a route that legitimately runs for minutes (a recipe generation
// waiting on the inference gate) or forever (an event stream) must extend or
// clear its own deadline with http.ResponseController. Those two cases are
// handled at their handlers; everything else is bounded here.
type Timeouts struct {
	ReadHeader     time.Duration
	Read           time.Duration
	Write          time.Duration
	Idle           time.Duration
	MaxHeaderBytes int
}

// The shipped deadlines.
const (
	// ReadHeaderTimeout bounds the request line and headers. This is the
	// slow-loris bound.
	ReadHeaderTimeout = 10 * time.Second
	// ReadTimeout bounds the whole request including the body. A 48 MB photo
	// batch over slow Wi-Fi needs real time, so this is generous rather than
	// tight.
	ReadTimeout = 120 * time.Second
	// WriteTimeout bounds handler execution plus the response write. Long
	// running routes extend or clear it themselves.
	WriteTimeout = 120 * time.Second
	// IdleTimeout bounds a kept-alive connection between requests.
	IdleTimeout = 120 * time.Second
	// MaxHeaderBytesLimit caps the header block.
	MaxHeaderBytesLimit = 1 << 20
	// LongInferenceWriteDeadline is what a handler that waits on the model
	// extends its write deadline to.
	//
	// The arithmetic it has to cover: up to config.DefaultInferQueueTimeout
	// (10 minutes) queued on the capacity-1 inference gate, plus up to
	// llm.DefaultRequestTimeout (10 minutes) in the call itself. 30 minutes is
	// that sum with room, and it is still a bound rather than none.
	LongInferenceWriteDeadline = 30 * time.Minute
)

// DefaultTimeouts returns the shipped deadlines.
func DefaultTimeouts() Timeouts {
	return Timeouts{
		ReadHeader:     ReadHeaderTimeout,
		Read:           ReadTimeout,
		Write:          WriteTimeout,
		Idle:           IdleTimeout,
		MaxHeaderBytes: MaxHeaderBytesLimit,
	}
}

func (t Timeouts) withDefaults() Timeouts {
	d := DefaultTimeouts()
	if t.ReadHeader <= 0 {
		t.ReadHeader = d.ReadHeader
	}
	if t.Read <= 0 {
		t.Read = d.Read
	}
	if t.Write <= 0 {
		t.Write = d.Write
	}
	if t.Idle <= 0 {
		t.Idle = d.Idle
	}
	if t.MaxHeaderBytes <= 0 {
		t.MaxHeaderBytes = d.MaxHeaderBytes
	}
	return t
}
