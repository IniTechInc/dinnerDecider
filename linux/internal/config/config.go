// Package config resolves the DinnerDecider service's runtime settings and
// hands the result to every other package.
//
// Four layers, lowest precedence first:
//
//	built-in defaults  <-  /etc/dinnerdecider/config.json  <-  DD_* environment  <-  command-line flags
//
// Each layer overrides only what it actually specifies, which is the whole
// difficulty. A Go zero value is indistinguishable from an omitted key, so a
// naive implementation has every layer "specify" every setting and the lowest
// layer wins by accident. Two mechanisms avoid that here:
//
//   - the file layer decodes into fileConfig, whose fields are all pointers, so
//     `"metered": false` is distinguishable from the key being absent;
//   - the flag layer is registered with the post-environment values as its flag
//     defaults, so an unset flag physically cannot clobber an environment
//     variable — there is no "was it set?" bookkeeping to get wrong.
//
// # File schema
//
// The config file is a flat JSON object keyed by the camelCase names below.
// Unknown keys are a hard error: a silently ignored typo looks exactly like a
// setting that does not work, and costs an afternoon to find.
//
//	key                  env                       flag                     default
//	bind                 DD_BIND                   -bind                    127.0.0.1
//	port                 DD_PORT                   -port                    8080
//	tlsPort              DD_TLS_PORT               -tls-port                8443
//	tls                  DD_TLS                    -tls                     selfsigned
//	tlsCertFile          DD_TLS_CERT_FILE          -tls-cert-file           ""
//	tlsKeyFile           DD_TLS_KEY_FILE           -tls-key-file            ""
//	auth                 DD_AUTH                   -auth                    token
//	stateDir             DD_STATE_DIR              -state-dir               /var/lib/dinnerdecider
//	modelsDir            DD_MODELS_DIR             -models-dir              "" (use the locator search order)
//	llamaServerPath      DD_LLAMA_SERVER_PATH      -llama-server-path       "" (look up on PATH)
//	scanCallBudget       DD_SCAN_CALL_BUDGET       -scan-call-budget        4
//	contextSize          DD_CONTEXT_SIZE           -context-size            1024
//	batchSize            DD_BATCH_SIZE             -batch-size              512
//	parallelSlots        DD_PARALLEL_SLOTS         -parallel-slots          1
//	idleUnloadMinutes    DD_IDLE_UNLOAD_MINUTES    -idle-unload-minutes     15
//	inferQueueTimeout    DD_INFER_QUEUE_TIMEOUT    -infer-queue-timeout     10m
//	ocr                  DD_OCR                    -ocr                     auto
//	metered              DD_METERED                -metered                 false
//	photoRetentionHours  DD_PHOTO_RETENTION_HOURS  -photo-retention-hours   24
//	maxUploadBytes       DD_MAX_UPLOAD_BYTES       -max-upload-bytes        50331648 (48 MiB)
//	maxUploadPixels      DD_MAX_UPLOAD_PIXELS      -max-upload-pixels       40000000
//	maxConcurrentScans   DD_MAX_CONCURRENT_SCANS   -max-concurrent-scans    2
//	maxSSEPerScan        DD_MAX_SSE_PER_SCAN       -max-sse-per-scan        16
//	maxSSETotal          DD_MAX_SSE_TOTAL          -max-sse-total           64
//
// Two settings are deliberately NOT readable from the config file:
//
//	-config                    the path to the file itself (also DD_CONFIG)
//	-i-accept-lan-exposure     the LAN-exposure acknowledgement (also DD_I_ACCEPT_LAN_EXPOSURE,
//	                           which docker-compose and systemd units need)
//
// An acknowledgement that a box is deliberately serving an unauthenticated
// inference endpoint to the LAN should be typed by an operator, not inherited
// from a config file copied off a forum post.
//
// Config carries no JSON struct tags. It is not the file schema — fileConfig
// is — and tagging it would invite a future marshal of Config to emit
// inferQueueTimeout as an integer count of nanoseconds.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
)

// DefaultConfigPath is the file layer's location when nothing overrides it.
// Its absence is not an error; an explicitly requested file's absence is.
const DefaultConfigPath = "/etc/dinnerdecider/config.json"

// EnvPrefix prefixes every environment variable this package reads.
const EnvPrefix = "DD_"

// TLSMode selects how the service obtains its server certificate.
type TLSMode string

const (
	// TLSOff serves plain HTTP only. Note the consequence recorded in
	// docs/PORTING_NOTES.md § "Known design constraint: browser camera access":
	// getUserMedia is gated to secure contexts, so a browser reaching this
	// service at http://192.168.x.x cannot open the camera at all. Loopback is
	// a secure context, so http://localhost is unaffected.
	TLSOff TLSMode = "off"
	// TLSSelfSigned generates a local CA and a leaf covering every detected
	// address (see package tlsgen). This is the default because it is the only
	// mode that keeps the camera working from a phone on the LAN, and it costs
	// one P-256 keygen at first start.
	TLSSelfSigned TLSMode = "selfsigned"
	// TLSFiles uses an operator-supplied certificate and key, for anyone who
	// already runs an internal CA or terminates with a real certificate.
	TLSFiles TLSMode = "files"
)

// AuthMode selects the service's authentication posture.
type AuthMode string

const (
	// AuthToken requires a paired session cookie or a bearer token. Default.
	AuthToken AuthMode = "token"
	// AuthNone disables authentication entirely. Permitted on a loopback bind;
	// on any other bind the service refuses to start without an explicit
	// acknowledgement. See package auth.
	AuthNone AuthMode = "none"
)

// OCRMode selects whether the optional tesseract exec is used.
type OCRMode string

const (
	// OCRAuto uses tesseract when it is on PATH and silently does without when
	// it is not, which the scan pipeline already tolerates.
	OCRAuto OCRMode = "auto"
	// OCROn requires tesseract; startup fails if it is missing.
	OCROn OCRMode = "on"
	// OCROff never execs tesseract. Measurably degrades packaged-goods recall,
	// so /api/status surfaces the effective mode.
	OCROff OCRMode = "off"
)

// Built-in defaults. Exported so tests and operator-facing help text cite one
// source rather than re-typing the numbers.
const (
	// DefaultBind is loopback, deliberately. Reaching the LAN is a decision an
	// operator makes, never one an install makes for them.
	DefaultBind = "127.0.0.1"
	// DefaultPort serves plain HTTP.
	DefaultPort = 8080
	// DefaultTLSPort serves HTTPS when TLS is not off.
	DefaultTLSPort = 8443
	// DefaultStateDir holds the database, photos, models, TLS material and the
	// auth token. Matches the model locator's /var/lib search entry.
	DefaultStateDir = "/var/lib/dinnerdecider"
	// DefaultContextSize is pinned at 1024 by the wired-memory work recorded in
	// shared/ (2048 -> 1536 -> 1024). Raising it is not a free knob.
	DefaultContextSize = 1024
	// DefaultBatchSize is 512. Batch 256 shipped on iOS and silently broke
	// recognition; the runtime arg guard refuses anything lower.
	DefaultBatchSize = 512
	// DefaultParallelSlots is 1 and is meant to stay 1. llama-server divides
	// --ctx-size across slots, so -np 2 at --ctx-size 1024 leaves 512 tokens per
	// slot — less than one image's vision tokens plus the prompt plus a reply.
	// Concurrency is admitted on the Go side instead.
	DefaultParallelSlots = 1
	// DefaultIdleUnloadMinutes unloads a resident model after this long idle.
	// The supervisor disables it outright on a box with >= 16 GiB, where the
	// residency costs nothing worth reclaiming.
	DefaultIdleUnloadMinutes = 15
	// DefaultInferQueueTimeout bounds how long a caller waits on the global
	// inference gate before giving up. A scan on a Pi already takes minutes, so
	// a queue wait is invisible; this exists only so a wedged holder cannot
	// block every caller forever.
	DefaultInferQueueTimeout = 10 * time.Minute
	// DefaultPhotoRetentionHours bounds how long uploaded photos survive.
	DefaultPhotoRetentionHours = 24
	// DefaultMaxUploadBytes caps a whole request body, enforced before parsing.
	DefaultMaxUploadBytes int64 = 48 << 20
	// DefaultMaxUploadPixels caps W*H, checked from the image header before any
	// decode, so a 64000x64000 decode bomb never allocates.
	DefaultMaxUploadPixels int64 = 40_000_000
	// DefaultMaxConcurrentScans allows one running scan plus one finished but
	// unconfirmed scan in the registry.
	DefaultMaxConcurrentScans = 2
	// DefaultMaxSSEPerScan caps event-stream subscribers on a single scan.
	DefaultMaxSSEPerScan = 16
	// DefaultMaxSSETotal caps event-stream subscribers service-wide.
	DefaultMaxSSETotal = 64
)

// Config is the fully resolved settings of one service process.
type Config struct {
	Bind    string
	Port    int
	TLSPort int

	TLS         TLSMode
	TLSCertFile string
	TLSKeyFile  string

	Auth AuthMode

	StateDir        string
	ModelsDir       string
	LlamaServerPath string

	ScanCallBudget    int
	ContextSize       int
	BatchSize         int
	ParallelSlots     int
	IdleUnloadMinutes int
	InferQueueTimeout time.Duration

	OCR                 OCRMode
	Metered             bool
	PhotoRetentionHours int

	MaxUploadBytes     int64
	MaxUploadPixels    int64
	MaxConcurrentScans int
	MaxSSEPerScan      int
	MaxSSETotal        int

	// AcceptLANExposure is the operator's explicit acknowledgement that an
	// unauthenticated service may bind a non-loopback address. Flag or
	// environment only — never the config file.
	AcceptLANExposure bool

	// ConfigPath records which file the file layer read, or would have read.
	// Empty only when the caller suppressed the file layer.
	ConfigPath string
}

// Defaults returns the built-in layer. Every other layer is applied on top of
// a copy of this.
func Defaults() Config {
	return Config{
		Bind:    DefaultBind,
		Port:    DefaultPort,
		TLSPort: DefaultTLSPort,

		TLS:  TLSSelfSigned,
		Auth: AuthToken,

		StateDir: DefaultStateDir,

		// ScanCallBudget is the one default that is not ours to choose: an 8 GB
		// phone was killed on the FIFTH heavy call of a scan, so the contract
		// caps a whole scan at four. Take the number from the contract rather
		// than re-typing it here.
		ScanCallBudget:    contract.DefaultScanCallLimit,
		ContextSize:       DefaultContextSize,
		BatchSize:         DefaultBatchSize,
		ParallelSlots:     DefaultParallelSlots,
		IdleUnloadMinutes: DefaultIdleUnloadMinutes,
		InferQueueTimeout: DefaultInferQueueTimeout,

		OCR:                 OCRAuto,
		Metered:             false,
		PhotoRetentionHours: DefaultPhotoRetentionHours,

		MaxUploadBytes:     DefaultMaxUploadBytes,
		MaxUploadPixels:    DefaultMaxUploadPixels,
		MaxConcurrentScans: DefaultMaxConcurrentScans,
		MaxSSEPerScan:      DefaultMaxSSEPerScan,
		MaxSSETotal:        DefaultMaxSSETotal,
	}
}

// TLSEnabled reports whether the service terminates TLS itself.
func (c Config) TLSEnabled() bool { return c.TLS != TLSOff }

// BindIsLoopback reports whether the configured bind address reaches only this
// machine.
func (c Config) BindIsLoopback() bool { return IsLoopbackBind(c.Bind) }

// TLSDir is where package tlsgen persists the CA and leaf.
func (c Config) TLSDir() string { return filepath.Join(c.StateDir, "tls") }

// AuthDir is where package auth persists the token hash.
func (c Config) AuthDir() string { return filepath.Join(c.StateDir, "auth") }

// PhotosDir is where uploaded photos live until the retention sweeper takes them.
func (c Config) PhotosDir() string { return filepath.Join(c.StateDir, "photos") }

// IsLoopbackBind reports whether a bind address reaches only this machine.
//
// It fails closed: anything it cannot positively identify as loopback — an
// empty string (which means "every interface"), 0.0.0.0, ::, or a hostname it
// will not resolve — is reported as NOT loopback. The one caller that matters
// is the startup guard that refuses an unauthenticated LAN bind, and a guard
// that guesses "probably local" is not a guard.
func IsLoopbackBind(bind string) bool {
	h := strings.TrimSpace(bind)
	if h == "" {
		return false
	}
	if strings.EqualFold(h, "localhost") {
		return true
	}
	// Accept a bracketed literal, as it appears in a host:port string.
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = h[1 : len(h)-1]
	}
	// Tolerate a zone on a link-local literal.
	if i := strings.IndexByte(h, '%'); i >= 0 {
		h = h[:i]
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

// Source describes where Load reads each layer from. The zero value reads the
// real environment, the real command line and the default config path, which is
// what the binary wants; tests fill it in.
type Source struct {
	// ConfigPath overrides the file layer's location. When set, the file must
	// exist. Set SkipFile instead of pointing this at /dev/null.
	ConfigPath string
	// SkipFile suppresses the file layer entirely.
	SkipFile bool
	// Env defaults to os.LookupEnv.
	Env func(string) (string, bool)
	// Args defaults to os.Args[1:].
	Args []string
	// Output receives flag-package usage and error text. Defaults to os.Stderr.
	Output io.Writer
}

func (s Source) env() func(string) (string, bool) {
	if s.Env != nil {
		return s.Env
	}
	return os.LookupEnv
}

func (s Source) args() []string {
	if s.Args != nil {
		return s.Args
	}
	if len(os.Args) > 0 {
		return os.Args[1:]
	}
	return nil
}

func (s Source) output() io.Writer {
	if s.Output != nil {
		return s.Output
	}
	return os.Stderr
}

// fileConfig is the config file's schema. Every field is a pointer so that an
// absent key and a present-but-zero key are distinguishable; without that,
// `"metered": false` and no metered key at all are the same bytes after
// decoding, and the file layer would silently re-assert defaults over nothing.
type fileConfig struct {
	Bind    *string `json:"bind"`
	Port    *int    `json:"port"`
	TLSPort *int    `json:"tlsPort"`

	TLS         *string `json:"tls"`
	TLSCertFile *string `json:"tlsCertFile"`
	TLSKeyFile  *string `json:"tlsKeyFile"`

	Auth *string `json:"auth"`

	StateDir        *string `json:"stateDir"`
	ModelsDir       *string `json:"modelsDir"`
	LlamaServerPath *string `json:"llamaServerPath"`

	ScanCallBudget    *int    `json:"scanCallBudget"`
	ContextSize       *int    `json:"contextSize"`
	BatchSize         *int    `json:"batchSize"`
	ParallelSlots     *int    `json:"parallelSlots"`
	IdleUnloadMinutes *int    `json:"idleUnloadMinutes"`
	InferQueueTimeout *string `json:"inferQueueTimeout"`

	OCR                 *string `json:"ocr"`
	Metered             *bool   `json:"metered"`
	PhotoRetentionHours *int    `json:"photoRetentionHours"`

	MaxUploadBytes     *int64 `json:"maxUploadBytes"`
	MaxUploadPixels    *int64 `json:"maxUploadPixels"`
	MaxConcurrentScans *int   `json:"maxConcurrentScans"`
	MaxSSEPerScan      *int   `json:"maxSSEPerScan"`
	MaxSSETotal        *int   `json:"maxSSETotal"`
}

func (f fileConfig) apply(c *Config) error {
	setString(f.Bind, &c.Bind)
	setInt(f.Port, &c.Port)
	setInt(f.TLSPort, &c.TLSPort)
	if f.TLS != nil {
		c.TLS = TLSMode(*f.TLS)
	}
	setString(f.TLSCertFile, &c.TLSCertFile)
	setString(f.TLSKeyFile, &c.TLSKeyFile)
	if f.Auth != nil {
		c.Auth = AuthMode(*f.Auth)
	}
	setString(f.StateDir, &c.StateDir)
	setString(f.ModelsDir, &c.ModelsDir)
	setString(f.LlamaServerPath, &c.LlamaServerPath)
	setInt(f.ScanCallBudget, &c.ScanCallBudget)
	setInt(f.ContextSize, &c.ContextSize)
	setInt(f.BatchSize, &c.BatchSize)
	setInt(f.ParallelSlots, &c.ParallelSlots)
	setInt(f.IdleUnloadMinutes, &c.IdleUnloadMinutes)
	if f.InferQueueTimeout != nil {
		d, err := time.ParseDuration(*f.InferQueueTimeout)
		if err != nil {
			return fmt.Errorf("inferQueueTimeout: %w", err)
		}
		c.InferQueueTimeout = d
	}
	if f.OCR != nil {
		c.OCR = OCRMode(*f.OCR)
	}
	if f.Metered != nil {
		c.Metered = *f.Metered
	}
	setInt(f.PhotoRetentionHours, &c.PhotoRetentionHours)
	setInt64(f.MaxUploadBytes, &c.MaxUploadBytes)
	setInt64(f.MaxUploadPixels, &c.MaxUploadPixels)
	setInt(f.MaxConcurrentScans, &c.MaxConcurrentScans)
	setInt(f.MaxSSEPerScan, &c.MaxSSEPerScan)
	setInt(f.MaxSSETotal, &c.MaxSSETotal)
	return nil
}

func setString(src *string, dst *string) {
	if src != nil {
		*dst = *src
	}
}

func setInt(src *int, dst *int) {
	if src != nil {
		*dst = *src
	}
}

func setInt64(src *int64, dst *int64) {
	if src != nil {
		*dst = *src
	}
}

// Load resolves all four layers and validates the result.
//
// It returns the configuration and any positional arguments left over after
// flag parsing — the subcommand words, so that `dinnerdecider token show` and
// `dinnerdecider --state-dir=/tmp/x token show` both work. Leading positional
// arguments are lifted out before flag parsing, because flag.Parse stops at the
// first non-flag argument and would otherwise leave every flag after a
// subcommand unparsed.
//
// A -h or -help request returns flag.ErrHelp unwrapped, so a caller can exit 0
// rather than reporting it as a failure.
func Load(src Source) (Config, []string, error) {
	cfg := Defaults()
	env := src.env()
	args := src.args()

	// Resolve the file layer's location first: the path itself is settable by
	// flag and environment, and that has to be decided before the file is read.
	path, explicit := resolveConfigPath(src, env, args)
	cfg.ConfigPath = path

	if !src.SkipFile && path != "" {
		if err := applyFile(&cfg, path, explicit); err != nil {
			return Config{}, nil, err
		}
	}

	if err := applyEnv(&cfg, env); err != nil {
		return Config{}, nil, err
	}

	positional, rest := splitLeadingPositional(args)
	tail, err := applyFlags(&cfg, rest, src.output())
	if err != nil {
		return Config{}, nil, err
	}
	positional = append(positional, tail...)

	if err := cfg.Validate(); err != nil {
		return Config{}, nil, err
	}
	return cfg, positional, nil
}

func resolveConfigPath(src Source, env func(string) (string, bool), args []string) (path string, explicit bool) {
	if src.ConfigPath != "" {
		return src.ConfigPath, true
	}
	if v, ok := scanConfigFlag(args); ok {
		return v, true
	}
	if v, ok := env(EnvPrefix + "CONFIG"); ok && v != "" {
		return v, true
	}
	return DefaultConfigPath, false
}

// scanConfigFlag pre-scans for -config before the real FlagSet exists. It
// handles both -config=value and -config value, in one- and two-dash forms.
func scanConfigFlag(args []string) (string, bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return "", false
		}
		for _, form := range []string{"--config=", "-config="} {
			if strings.HasPrefix(a, form) {
				return strings.TrimPrefix(a, form), true
			}
		}
		if a == "--config" || a == "-config" {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", false
		}
	}
	return "", false
}

func applyFile(cfg *Config, path string, explicit bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !explicit {
			// The built-in path not existing is the normal case for a
			// development run and for a package that ships no config file.
			return nil
		}
		return fmt.Errorf("config file %s: %w", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	// A typo'd key that is silently ignored is indistinguishable from a setting
	// that does not work. Refuse it at startup, where it is cheap to find.
	dec.DisallowUnknownFields()
	var fc fileConfig
	if err := dec.Decode(&fc); err != nil {
		return fmt.Errorf("config file %s: %w", path, err)
	}
	// json.Decoder stops at the end of the first value, so a second object or
	// stray text after the config would otherwise be accepted in silence.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("config file %s: trailing data after the top-level object", path)
	}
	if err := fc.apply(cfg); err != nil {
		return fmt.Errorf("config file %s: %w", path, err)
	}
	return nil
}

func applyEnv(cfg *Config, env func(string) (string, bool)) error {
	var errs []error
	str := func(key string, dst *string) {
		if v, ok := env(EnvPrefix + key); ok {
			*dst = v
		}
	}
	num := func(key string, dst *int) {
		v, ok := env(EnvPrefix + key)
		if !ok {
			return
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s%s: %q is not an integer", EnvPrefix, key, v))
			return
		}
		*dst = n
	}
	num64 := func(key string, dst *int64) {
		v, ok := env(EnvPrefix + key)
		if !ok {
			return
		}
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s%s: %q is not an integer", EnvPrefix, key, v))
			return
		}
		*dst = n
	}
	boolean := func(key string, dst *bool) {
		v, ok := env(EnvPrefix + key)
		if !ok {
			return
		}
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s%s: %q is not a boolean", EnvPrefix, key, v))
			return
		}
		*dst = b
	}

	str("BIND", &cfg.Bind)
	num("PORT", &cfg.Port)
	num("TLS_PORT", &cfg.TLSPort)
	if v, ok := env(EnvPrefix + "TLS"); ok {
		cfg.TLS = TLSMode(v)
	}
	str("TLS_CERT_FILE", &cfg.TLSCertFile)
	str("TLS_KEY_FILE", &cfg.TLSKeyFile)
	if v, ok := env(EnvPrefix + "AUTH"); ok {
		cfg.Auth = AuthMode(v)
	}
	str("STATE_DIR", &cfg.StateDir)
	str("MODELS_DIR", &cfg.ModelsDir)
	str("LLAMA_SERVER_PATH", &cfg.LlamaServerPath)
	num("SCAN_CALL_BUDGET", &cfg.ScanCallBudget)
	num("CONTEXT_SIZE", &cfg.ContextSize)
	num("BATCH_SIZE", &cfg.BatchSize)
	num("PARALLEL_SLOTS", &cfg.ParallelSlots)
	num("IDLE_UNLOAD_MINUTES", &cfg.IdleUnloadMinutes)
	if v, ok := env(EnvPrefix + "INFER_QUEUE_TIMEOUT"); ok {
		d, err := time.ParseDuration(strings.TrimSpace(v))
		if err != nil {
			errs = append(errs, fmt.Errorf("%sINFER_QUEUE_TIMEOUT: %w", EnvPrefix, err))
		} else {
			cfg.InferQueueTimeout = d
		}
	}
	if v, ok := env(EnvPrefix + "OCR"); ok {
		cfg.OCR = OCRMode(v)
	}
	boolean("METERED", &cfg.Metered)
	num("PHOTO_RETENTION_HOURS", &cfg.PhotoRetentionHours)
	num64("MAX_UPLOAD_BYTES", &cfg.MaxUploadBytes)
	num64("MAX_UPLOAD_PIXELS", &cfg.MaxUploadPixels)
	num("MAX_CONCURRENT_SCANS", &cfg.MaxConcurrentScans)
	num("MAX_SSE_PER_SCAN", &cfg.MaxSSEPerScan)
	num("MAX_SSE_TOTAL", &cfg.MaxSSETotal)
	boolean("I_ACCEPT_LAN_EXPOSURE", &cfg.AcceptLANExposure)

	return errors.Join(errs...)
}

func applyFlags(cfg *Config, args []string, out io.Writer) ([]string, error) {
	fs := flag.NewFlagSet("dinnerdecider", flag.ContinueOnError)
	fs.SetOutput(out)

	// Every flag's default is the post-environment value, so an unset flag is
	// literally a no-op. There is no "was this flag set?" bookkeeping to get
	// wrong, which is where layered-config implementations usually break.
	tlsMode := string(cfg.TLS)
	authMode := string(cfg.Auth)
	ocrMode := string(cfg.OCR)

	fs.String("config", cfg.ConfigPath, "path to the JSON config file (also "+EnvPrefix+"CONFIG)")
	fs.StringVar(&cfg.Bind, "bind", cfg.Bind, "address to bind (loopback by default; a LAN bind is an explicit decision)")
	fs.IntVar(&cfg.Port, "port", cfg.Port, "plain HTTP port")
	fs.IntVar(&cfg.TLSPort, "tls-port", cfg.TLSPort, "HTTPS port, used unless -tls=off")
	fs.StringVar(&tlsMode, "tls", tlsMode, "TLS mode: off|selfsigned|files")
	fs.StringVar(&cfg.TLSCertFile, "tls-cert-file", cfg.TLSCertFile, "certificate file, required when -tls=files")
	fs.StringVar(&cfg.TLSKeyFile, "tls-key-file", cfg.TLSKeyFile, "private key file, required when -tls=files")
	fs.StringVar(&authMode, "auth", authMode, "authentication mode: token|none")
	fs.StringVar(&cfg.StateDir, "state-dir", cfg.StateDir, "database, photos, models, TLS material and auth token")
	fs.StringVar(&cfg.ModelsDir, "models-dir", cfg.ModelsDir, "model directory; empty uses the locator search order")
	fs.StringVar(&cfg.LlamaServerPath, "llama-server-path", cfg.LlamaServerPath, "llama-server binary; empty looks it up on PATH")
	fs.IntVar(&cfg.ScanCallBudget, "scan-call-budget", cfg.ScanCallBudget, "heavy model calls per scan (4 is a memory limit, not a tuning knob)")
	fs.IntVar(&cfg.ContextSize, "context-size", cfg.ContextSize, "llama-server --ctx-size")
	fs.IntVar(&cfg.BatchSize, "batch-size", cfg.BatchSize, "llama-server --batch-size")
	fs.IntVar(&cfg.ParallelSlots, "parallel-slots", cfg.ParallelSlots, "llama-server --parallel (1 is deliberate; see the arg guard)")
	fs.IntVar(&cfg.IdleUnloadMinutes, "idle-unload-minutes", cfg.IdleUnloadMinutes, "unload an idle model after this long; 0 disables")
	fs.DurationVar(&cfg.InferQueueTimeout, "infer-queue-timeout", cfg.InferQueueTimeout, "how long a caller waits on the global inference gate")
	fs.StringVar(&ocrMode, "ocr", ocrMode, "OCR mode: auto|on|off")
	fs.BoolVar(&cfg.Metered, "metered", cfg.Metered, "treat the network connection as metered")
	fs.IntVar(&cfg.PhotoRetentionHours, "photo-retention-hours", cfg.PhotoRetentionHours, "how long uploaded photos survive; 0 sweeps at terminal state")
	fs.Int64Var(&cfg.MaxUploadBytes, "max-upload-bytes", cfg.MaxUploadBytes, "whole-request body cap, enforced before parsing")
	fs.Int64Var(&cfg.MaxUploadPixels, "max-upload-pixels", cfg.MaxUploadPixels, "W*H cap, checked from the image header before decoding")
	fs.IntVar(&cfg.MaxConcurrentScans, "max-concurrent-scans", cfg.MaxConcurrentScans, "scan registry cap")
	fs.IntVar(&cfg.MaxSSEPerScan, "max-sse-per-scan", cfg.MaxSSEPerScan, "event-stream subscribers per scan")
	fs.IntVar(&cfg.MaxSSETotal, "max-sse-total", cfg.MaxSSETotal, "event-stream subscribers service-wide")
	fs.BoolVar(&cfg.AcceptLANExposure, "i-accept-lan-exposure", cfg.AcceptLANExposure,
		"acknowledge serving an unauthenticated inference endpoint to the LAN; required with -auth=none on a non-loopback bind")

	if err := fs.Parse(args); err != nil {
		// flag.ErrHelp is a request, not a failure. Pass it through unwrapped
		// so a caller can exit 0 on -h.
		if errors.Is(err, flag.ErrHelp) {
			return nil, flag.ErrHelp
		}
		return nil, err
	}

	cfg.TLS = TLSMode(tlsMode)
	cfg.Auth = AuthMode(authMode)
	cfg.OCR = OCRMode(ocrMode)
	return fs.Args(), nil
}

// splitLeadingPositional lifts leading non-flag arguments out of args so that a
// subcommand does not stop flag parsing. `token show -state-dir=/x` and
// `-state-dir=/x token show` both end up with the same flags and the same two
// positional words.
func splitLeadingPositional(args []string) (positional, rest []string) {
	i := 0
	for i < len(args) && args[i] != "--" && !strings.HasPrefix(args[i], "-") {
		i++
	}
	return append([]string(nil), args[:i]...), args[i:]
}

// Validate rejects a configuration that cannot work, at startup, where the
// operator is watching — rather than at the first request, where they are not.
func (c Config) Validate() error {
	var errs []error

	switch c.TLS {
	case TLSOff, TLSSelfSigned, TLSFiles:
	default:
		errs = append(errs, fmt.Errorf("tls: %q is not one of off, selfsigned, files", string(c.TLS)))
	}
	if c.TLS == TLSFiles {
		if c.TLSCertFile == "" || c.TLSKeyFile == "" {
			errs = append(errs, errors.New(`tls: mode "files" needs both tlsCertFile and tlsKeyFile`))
		}
	}

	switch c.Auth {
	case AuthToken, AuthNone:
	default:
		errs = append(errs, fmt.Errorf("auth: %q is not one of token, none", string(c.Auth)))
	}

	switch c.OCR {
	case OCRAuto, OCROn, OCROff:
	default:
		errs = append(errs, fmt.Errorf("ocr: %q is not one of auto, on, off", string(c.OCR)))
	}

	if c.Port < 0 || c.Port > 65535 {
		errs = append(errs, fmt.Errorf("port: %d is out of range 0-65535", c.Port))
	}
	if c.TLSPort < 0 || c.TLSPort > 65535 {
		errs = append(errs, fmt.Errorf("tlsPort: %d is out of range 0-65535", c.TLSPort))
	}
	if c.TLSEnabled() && c.Port != 0 && c.Port == c.TLSPort {
		errs = append(errs, fmt.Errorf("port and tlsPort are both %d; one process cannot bind the same port twice", c.Port))
	}

	if c.StateDir == "" {
		errs = append(errs, errors.New("stateDir: must not be empty"))
	}

	positive := func(name string, v int) {
		if v < 1 {
			errs = append(errs, fmt.Errorf("%s: %d must be at least 1", name, v))
		}
	}
	positive("scanCallBudget", c.ScanCallBudget)
	positive("contextSize", c.ContextSize)
	positive("batchSize", c.BatchSize)
	positive("parallelSlots", c.ParallelSlots)
	positive("maxConcurrentScans", c.MaxConcurrentScans)
	positive("maxSSEPerScan", c.MaxSSEPerScan)
	positive("maxSSETotal", c.MaxSSETotal)

	if c.IdleUnloadMinutes < 0 {
		errs = append(errs, fmt.Errorf("idleUnloadMinutes: %d must not be negative (0 disables idle unload)", c.IdleUnloadMinutes))
	}
	if c.PhotoRetentionHours < 0 {
		errs = append(errs, fmt.Errorf("photoRetentionHours: %d must not be negative (0 sweeps at terminal state)", c.PhotoRetentionHours))
	}
	if c.InferQueueTimeout <= 0 {
		errs = append(errs, fmt.Errorf("inferQueueTimeout: %s must be positive", c.InferQueueTimeout))
	}
	if c.MaxUploadBytes < 1 {
		errs = append(errs, fmt.Errorf("maxUploadBytes: %d must be at least 1", c.MaxUploadBytes))
	}
	if c.MaxUploadPixels < 1 {
		errs = append(errs, fmt.Errorf("maxUploadPixels: %d must be at least 1", c.MaxUploadPixels))
	}

	return errors.Join(errs...)
}
