package api

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/IniTechInc/dinnerDecider/linux/internal/auth"
	"github.com/IniTechInc/dinnerDecider/linux/internal/config"
	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
	"github.com/IniTechInc/dinnerDecider/linux/internal/llm"
	"github.com/IniTechInc/dinnerDecider/linux/internal/model"
	"github.com/IniTechInc/dinnerDecider/linux/internal/store"
	"github.com/IniTechInc/dinnerDecider/linux/internal/tlsgen"
)

// ModelStatus is the API's read-only view of the inference supervisor.
// *llm.Supervisor satisfies it.
type ModelStatus interface {
	Status() llm.Status
}

// BuildInfo identifies this binary, for GET /api/status.
type BuildInfo struct {
	Version   string
	Commit    string
	BuildDate string
}

// Deps is everything the HTTP surface needs. It is filled by the service's
// composition root, which is the only place that knows about all of these at
// once.
type Deps struct {
	// DB and Photos are required.
	DB     *store.DB
	Photos *store.PhotoStore
	// Scans and Recipes are required.
	Scans   ScanManager
	Recipes RecipeGenerator

	// Model, Downloads and ModelEvents are optional: a service running against
	// the mock backend, or started with downloading switched off, has none of
	// them and still serves everything else.
	Model       ModelStatus
	Downloads   ModelDownloader
	ModelEvents *ModelHub
	Locator     model.Locator

	// Auth is optional only in tests. In the service it is always present, and
	// auth=none is expressed inside it rather than by leaving it nil.
	Auth *auth.Authenticator
	// CACertPEM is served unauthenticated at /ca.crt so a client can trust the
	// connection before it has a credential.
	CACertPEM []byte
	// UI is the embedded web interface (step 65). Nil serves a placeholder.
	UI http.Handler

	Config  config.Config
	Kroger  KrogerConfig
	OCRMode string
	Target  llm.Target

	// Memory answers the degraded question on GET /api/status. It defaults to
	// llm.CheckMemory, which reads /proc/meminfo and therefore fails on a
	// developer's macOS box; that failure is reported as "unknown" rather than
	// as degraded.
	Memory func() (llm.MemoryStatus, error)

	Build    BuildInfo
	Limits   Limits
	Timeouts Timeouts
	Now      func() time.Time
	Logf     func(format string, args ...any)
}

// Router is the whole HTTP surface.
type Router struct {
	mux      *http.ServeMux
	handler  http.Handler
	deps     Deps
	limits   Limits
	timeouts Timeouts
	logf     func(format string, args ...any)
	now      func() time.Time

	scan      *scanAPI
	inventory *inventoryAPI
	recipes   *recipesAPI
	shopping  *shoppingAPI
	settings  *settingsAPI
	model     *modelAPI
	kroger    *krogerAPI
}

// ContentSecurityPolicy is the policy every response carries.
//
// It is not decoration. This service ships its own fonts, stylesheets and
// scripts and reaches no external origin by design (the whole product is
// on-device inference in a house). default-src 'self' turns that requirement
// from a rule in a document into something the BROWSER enforces at runtime and
// a test can assert: the day somebody adds a CDN <script>, the page breaks
// loudly here rather than quietly phoning home from a kitchen.
//
// The two relaxations are the minimum that a working UI needs:
//
//	img-src ... data: blob:  the camera path draws a capture to a <canvas> and
//	                         hands it back as a blob or data URL, and staged
//	                         photo thumbnails are rendered the same way.
//	style-src ... 'unsafe-inline'  inline style attributes for progress bars and
//	                         the like. Scripts are NOT given this.
const ContentSecurityPolicy = "default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'"

// New builds the router and registers every route.
func New(deps Deps) (*Router, error) {
	if deps.DB == nil {
		return nil, errors.New("api: a database is required")
	}
	if deps.Photos == nil {
		return nil, errors.New("api: a photo store is required")
	}
	if deps.Scans == nil {
		return nil, errors.New("api: a scan manager is required")
	}

	limits := deps.Limits.withDefaults()
	timeouts := deps.Timeouts.withDefaults()
	logf := deps.Logf
	if logf == nil {
		logf = log.Printf
	}
	now := nowOrTime(deps.Now)
	if deps.Memory == nil {
		deps.Memory = llm.CheckMemory
	}

	shared := base{
		limits: limits,
		sse:    newSSEGate(limits.MaxSSETotal),
		logf:   logf,
		now:    now,
	}

	rt := &Router{
		mux:      http.NewServeMux(),
		deps:     deps,
		limits:   limits,
		timeouts: timeouts,
		logf:     logf,
		now:      now,
	}
	rt.scan = &scanAPI{base: shared, scans: deps.Scans, photos: deps.Photos, db: deps.DB}
	rt.inventory = &inventoryAPI{base: shared, db: deps.DB}
	rt.recipes = &recipesAPI{base: shared, db: deps.DB, engine: deps.Recipes}
	rt.shopping = &shoppingAPI{base: shared, db: deps.DB}
	rt.settings = &settingsAPI{base: shared, db: deps.DB}
	rt.model = &modelAPI{base: shared, downloads: deps.Downloads, hub: deps.ModelEvents, locator: deps.Locator, db: deps.DB}
	rt.kroger = &krogerAPI{
		base:        shared,
		config:      deps.Kroger,
		redirectURI: krogerRedirectURI(rt.scheme(), deps.Config.Bind, rt.port()),
	}

	rt.routes()
	rt.handler = securityHeaders(rt.authenticated(rt.mux))
	return rt, nil
}

// routes is the complete route table. THIS IS THE ONLY PLACE ROUTES ARE
// REGISTERED, and it is written once: a route table spread across the files
// that implement it is a route table nobody can read, and the security posture
// of an endpoint (is it exempt from authentication? does it stream?) is a
// property of the table, not of the handler.
//
// Patterns use the Go 1.22 method-and-wildcard syntax, so {id} is extracted
// with r.PathValue rather than by splitting strings.
//
// The mux's own 405-with-Allow does NOT survive the "/api/" catch-all at the
// bottom of this table: a DELETE to /api/settings still MATCHES "/api/", so the
// mux dispatches there instead of concluding that nothing matched. The
// catch-all therefore has to answer 405 itself, which is what apiFallback does.
func (rt *Router) routes() {
	m := rt.mux

	// --- Unauthenticated -----------------------------------------------
	// Three exemptions, each with a reason. The UI shell carries no data and
	// must load so the pairing screen can be shown; a liveness probe has no
	// credential and one that can fail for authentication reasons is worse than
	// useless; and requiring a credential to fetch the certificate you need in
	// order to trust the connection is a bootstrap deadlock. internal/auth owns
	// the list; these are the routes behind it.
	// Registered without a method so it does not conflict with the "/api/"
	// catch-all below. Go 1.22 rejects "GET /" against "/api/" as ambiguous:
	// /api/ matches more methods but has the more specific path. Method
	// filtering therefore happens in the handler.
	m.Handle("/", rt.uiRoot())
	m.HandleFunc("GET /healthz", rt.handleHealthz)
	m.Handle("GET /ca.crt", rt.caCert())
	m.Handle("POST /api/pair", rt.pair())

	// --- Status ---------------------------------------------------------
	m.HandleFunc("GET /api/status", rt.handleStatus)

	// --- Photos ---------------------------------------------------------
	m.HandleFunc("POST /api/photos", rt.scan.handleUploadPhotos)
	m.HandleFunc("DELETE /api/photos/{id}", rt.scan.handleDeletePhoto)

	// --- Scan lifecycle --------------------------------------------------
	// POST answers 409 with the running scan's id when one is already going,
	// so a second client attaches rather than starting a second scan.
	m.HandleFunc("POST /api/scan", rt.scan.handleStartScan)
	m.HandleFunc("GET /api/scan/{id}", rt.scan.handleScanSnapshot)
	m.HandleFunc("GET /api/scan/{id}/events", rt.scan.handleScanEvents) // SSE
	m.HandleFunc("DELETE /api/scan/{id}", rt.scan.handleDeleteScan)
	m.HandleFunc("POST /api/scan/{id}/confirm", rt.scan.handleConfirmScan)

	// --- Inventory -------------------------------------------------------
	m.HandleFunc("GET /api/inventory", rt.inventory.handleList)
	m.HandleFunc("POST /api/inventory", rt.inventory.handleCreate)
	m.HandleFunc("PATCH /api/inventory/{id}", rt.inventory.handleUpdate)
	m.HandleFunc("DELETE /api/inventory/{id}", rt.inventory.handleDelete)
	m.HandleFunc("POST /api/cook", rt.inventory.handleCook)

	// --- Recipes ---------------------------------------------------------
	// Deliberately NOT admitted against a running scan: nesting is contractual
	// and this queues on the supervisor's capacity-1 inference gate.
	m.HandleFunc("POST /api/recipes", rt.recipes.handleGenerate)

	// --- Shopping ---------------------------------------------------------
	m.HandleFunc("GET /api/shopping", rt.shopping.handleList)
	m.HandleFunc("POST /api/shopping", rt.shopping.handleCreate)
	m.HandleFunc("GET /api/shopping/export", rt.shopping.handleExport)
	m.HandleFunc("PATCH /api/shopping/{id}", rt.shopping.handleUpdate)
	m.HandleFunc("DELETE /api/shopping/{id}", rt.shopping.handleDelete)

	// --- Settings ---------------------------------------------------------
	m.HandleFunc("GET /api/settings", rt.settings.handleGet)
	m.HandleFunc("PUT /api/settings", rt.settings.handlePut)
	m.HandleFunc("GET /api/taste-profile", rt.settings.handleGetTasteProfile)
	m.HandleFunc("PUT /api/taste-profile", rt.settings.handlePutTasteProfile)

	// --- Model files -------------------------------------------------------
	m.HandleFunc("GET /api/model", rt.model.handleGet)
	m.HandleFunc("GET /api/model/events", rt.model.handleEvents) // SSE
	m.HandleFunc("POST /api/model/{action}", rt.model.handleAction)

	// --- Kroger ------------------------------------------------------------
	// Dormant when unconfigured, which is the normal state: there is no
	// first-party client id for this port. See kroger.go.
	m.HandleFunc("GET /api/kroger/status", rt.kroger.handleStatus)

	// A JSON 404 (or 405) for anything else under /api/. Without this the "GET /"
	// pattern catches a mistyped API path and answers with the HTML shell,
	// which a client parses as JSON and reports as a baffling syntax error.
	m.HandleFunc("/api/", rt.apiFallback)
}

// apiCatchAllPattern is the pattern registered for the /api/ catch-all. It is
// what mux.Handler reports when a probe matched nothing more specific.
const apiCatchAllPattern = "/api/"

// probeMethods is the set apiFallback offers, in the order an Allow header
// should list them. OPTIONS is absent because no route registers it.
var probeMethods = []string{
	http.MethodGet,
	http.MethodHead,
	http.MethodPost,
	http.MethodPut,
	http.MethodPatch,
	http.MethodDelete,
}

// apiFallback answers everything under /api/ that no route claimed.
//
// A path that IS routed, reached by a verb it does not accept, gets 405 with an
// Allow header rather than 404: the difference tells a client "you used the
// wrong method" instead of "you invented an endpoint", and those are different
// bugs to go fix. Anything genuinely unrouted still gets the JSON 404.
//
// The allowed set is derived by asking the mux rather than from a second table
// of paths kept in step by hand, so a route added above is covered here the
// moment it is registered.
func (rt *Router) apiFallback(w http.ResponseWriter, r *http.Request) {
	if allow := rt.allowedMethods(r); len(allow) > 0 {
		w.Header().Set("Allow", strings.Join(allow, ", "))
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed",
			fmt.Sprintf("That address does not accept %s. It accepts %s.", r.Method, strings.Join(allow, ", ")))
		return
	}
	notFound(w, r)
}

// allowedMethods re-dispatches this request's path against the mux once per
// verb and collects the ones that reach a real route.
//
// Note that a GET pattern answers a HEAD probe too, because the mux matches
// HEAD against GET, so HEAD is reported wherever GET is. That is what RFC 9110
// asks for.
func (rt *Router) allowedMethods(r *http.Request) []string {
	var allow []string
	for _, method := range probeMethods {
		if method == r.Method {
			continue
		}
		probe := r.Clone(r.Context())
		probe.Method = method
		if _, pattern := rt.mux.Handler(probe); pattern != "" && pattern != apiCatchAllPattern {
			allow = append(allow, method)
		}
	}
	return allow
}

// uiRoot serves the embedded web UI, rejecting methods it cannot answer.
//
// The route is registered without a method (see routes) so that the "/api/"
// catch-all can win for API paths, which means every verb arrives here. A
// mistyped POST to "/" should say so rather than silently returning the shell.
func (rt *Router) uiRoot() http.Handler {
	ui := rt.ui()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			ui.ServeHTTP(w, r)
		default:
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

// Handler is the fully wrapped handler: security headers outside
// authentication, so a 401 carries them too.
func (rt *Router) Handler() http.Handler { return rt.handler }

// Server builds the http.Server with its deadlines set.
//
// GO'S DEFAULTS HERE ARE UNLIMITED, which on a LAN-exposed box is an open
// invitation: a client that opens a connection and dribbles one header byte a
// minute holds a goroutine and a file descriptor forever, and a few hundred of
// them hold the whole service. Every one of these is set for that reason.
//
//	ReadHeaderTimeout  the slow-loris bound proper.
//	ReadTimeout        the whole request including a 48 MB photo batch.
//	WriteTimeout       handler execution plus the response write.
//	IdleTimeout        a kept-alive connection between requests.
//	MaxHeaderBytes     the header block.
//
// THE TWO SSE ROUTES RUN WITH NO WRITE DEADLINE. A stream never ends, so any
// finite write deadline kills a healthy connection in the middle of a
// four-minute scan. Rather than split the service across two listeners, the
// stream handlers clear the deadline per connection with
// http.ResponseController (see newSSEStream), which is the same thing as
// WriteTimeout: 0 for those routes and nothing else.
//
// The consequence is the reason limits.go has the caps it has: once cleared,
// nothing in net/http will ever reap that connection, so the idle deadline, the
// keepalive and the subscriber caps are the entire replacement. POST
// /api/recipes extends rather than clears its deadline, because it waits on the
// inference gate but does eventually answer.
func (rt *Router) Server(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           rt.Handler(),
		ReadHeaderTimeout: rt.timeouts.ReadHeader,
		ReadTimeout:       rt.timeouts.Read,
		WriteTimeout:      rt.timeouts.Write,
		IdleTimeout:       rt.timeouts.Idle,
		MaxHeaderBytes:    rt.timeouts.MaxHeaderBytes,
		ErrorLog:          log.New(logWriter{rt.logf}, "", 0),
	}
}

// logWriter routes net/http's own error log through the service's logger.
type logWriter struct {
	logf func(format string, args ...any)
}

func (w logWriter) Write(p []byte) (int, error) {
	w.logf("[HTTP] %s", string(p))
	return len(p), nil
}

// securityHeaders applies the policy to every response, including the ones
// authentication refuses.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", ContentSecurityPolicy)
		// Without nosniff a browser is free to decide that a photo the user
		// uploaded is actually a script, and run it from this origin.
		h.Set("X-Content-Type-Options", "nosniff")
		// This service is not a frame for anyone, ever. Being framed by a page
		// on the LAN is how a clickjack reaches a pair button.
		h.Set("X-Frame-Options", "DENY")
		// Nothing outside is contacted, so a referrer has nowhere useful to go
		// and would only leak scan ids into a URL bar.
		h.Set("Referrer-Policy", "no-referrer")
		// No reason for a page here to use a camera or microphone through a
		// permissions-policy-governed API; the capture path is a file input.
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// authenticated wraps the mux in the authenticator, when there is one.
func (rt *Router) authenticated(next http.Handler) http.Handler {
	if rt.deps.Auth == nil {
		return next
	}
	return rt.deps.Auth.Middleware(next)
}

func (rt *Router) pair() http.Handler {
	if rt.deps.Auth == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusConflict, "auth_disabled",
				"This service is running with authentication disabled; there is nothing to pair.")
		})
	}
	return rt.deps.Auth.PairHandler()
}

func (rt *Router) caCert() http.Handler {
	if len(rt.deps.CACertPEM) == 0 {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusNotFound, "no_ca",
				"This service is not terminating TLS, so there is no certificate to install.")
		})
	}
	return tlsgen.CACertHandler(rt.deps.CACertPEM)
}

func (rt *Router) ui() http.Handler {
	if rt.deps.UI != nil {
		return rt.deps.UI
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("The web interface is not built into this binary. The JSON API is under /api/.\n"))
	})
}

// handleHealthz is the liveness probe: unauthenticated, cheap, and it answers
// about the PROCESS rather than about the model. A probe that went red while
// the model was unloaded would have systemd restart a service that is working
// exactly as designed.
func (rt *Router) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// statusResponse is GET /api/status.
type statusResponse struct {
	Service  string               `json:"service"`
	Version  string               `json:"version"`
	Commit   string               `json:"commit,omitempty"`
	Built    string               `json:"built,omitempty"`
	Go       string               `json:"go"`
	Model    statusModel          `json:"model"`
	Hardware statusHardware       `json:"hardware"`
	Memory   statusMemory         `json:"memory"`
	OCRMode  string               `json:"ocrMode"`
	Auth     string               `json:"auth"`
	Kroger   krogerStatus         `json:"kroger"`
	Scan     statusScan           `json:"scan"`
	Limits   statusLimits         `json:"limits"`
	Phases   []contract.ScanPhase `json:"scanPhases"`
}

type statusModel struct {
	// State is the lifecycle state: unloaded, loading, ready, inferring,
	// unloading.
	State string `json:"state"`
	// LoadPhase is the small load-phase UI state.
	LoadPhase string `json:"loadPhase,omitempty"`
	// ActiveSessions can legitimately be 2: a recipe generation nests inside or
	// overlaps a scan.
	ActiveSessions int  `json:"activeSessions"`
	PendingUnload  bool `json:"pendingUnload"`
	// Variant and RuntimeSource identify the llama-server build in use.
	Variant       string `json:"variant,omitempty"`
	RuntimeSource string `json:"runtimeSource,omitempty"`
	// FellBackToCPU is surfaced because it is the difference between a scan
	// taking one minute and taking fifteen, and the operator otherwise has no
	// way to know it happened.
	FellBackToCPU  bool   `json:"fellBackToCpu"`
	FallbackReason string `json:"fallbackReason,omitempty"`
	Recovering     bool   `json:"recovering"`
	RecoveryUsed   bool   `json:"recoveryUsed"`
	// DemoMode means the service is answering with the mock backend because the
	// model files are not installed. The UI shows an orange badge for it: never
	// present canned data as real recognition.
	DemoMode   bool   `json:"demoMode"`
	DemoReason string `json:"demoReason,omitempty"`
	// Installed is what the filesystem says, independent of any download.
	Installed modelInstalledStatus `json:"installed"`
	Stats     llm.Stats            `json:"stats"`
}

type statusHardware struct {
	GOARCH      string `json:"goarch"`
	Accelerator string `json:"accelerator"`
	// AssetVariant is empty when there is no official llama-server build for
	// this architecture, which is itself the answer to "why is this box using
	// the mock".
	AssetVariant     string `json:"assetVariant,omitempty"`
	HasOfficialBuild bool   `json:"hasOfficialBuild"`
}

type statusMemory struct {
	// Known is false when the memory status could not be read, which is the
	// case on any platform without /proc/meminfo. Unknown is reported as
	// unknown rather than as healthy or as degraded.
	Known          bool   `json:"known"`
	TotalBytes     uint64 `json:"totalBytes,omitempty"`
	AvailableBytes uint64 `json:"availableBytes,omitempty"`
	CanLoad        bool   `json:"canLoad"`
	// Degraded marks a machine that can load the model but has no headroom.
	// Surfaced so an operator sees why scans are slow instead of guessing.
	Degraded bool   `json:"degraded"`
	Message  string `json:"message,omitempty"`
}

type statusScan struct {
	// ActiveScanID is the running scan, if any. A client that gets a 409 from
	// POST /api/scan, or that has just loaded the page, attaches to this.
	ActiveScanID string `json:"activeScanID,omitempty"`
	Running      bool   `json:"running"`
	Scans        int    `json:"scans"`
	Subscribers  int    `json:"streamSubscribers"`
}

type statusLimits struct {
	MaxRequestBytes    int64 `json:"maxRequestBytes"`
	MaxFileBytes       int64 `json:"maxFileBytes"`
	MaxFilesPerRequest int   `json:"maxFilesPerRequest"`
	MaxUploadPixels    int64 `json:"maxUploadPixels"`
	MaxConcurrentScans int   `json:"maxConcurrentScans"`
	MaxSSEPerScan      int   `json:"maxSsePerScan"`
	MaxSSETotal        int   `json:"maxSseTotal"`
}

// handleStatus is the one call a client makes to find out what this service can
// currently do.
func (rt *Router) handleStatus(w http.ResponseWriter, r *http.Request) {
	installed := rt.model.installed(r)

	statusModelValue := statusModel{
		State:     contract.Unloaded.String(),
		Installed: installed,
	}
	if rt.deps.Model != nil {
		s := rt.deps.Model.Status()
		statusModelValue.State = s.State.String()
		statusModelValue.LoadPhase = string(s.LoadPhase)
		statusModelValue.ActiveSessions = s.ActiveSessions
		statusModelValue.PendingUnload = s.PendingUnload
		statusModelValue.Variant = s.Variant
		statusModelValue.RuntimeSource = string(s.RuntimeSource)
		statusModelValue.FellBackToCPU = s.FellBackToCPU
		statusModelValue.FallbackReason = s.FallbackReason
		statusModelValue.Recovering = s.Recovering
		statusModelValue.RecoveryUsed = s.RecoveryUsed
		statusModelValue.Stats = s.Stats
	}
	statusModelValue.DemoMode, statusModelValue.DemoReason = rt.demoMode(installed)

	activeID, running := rt.deps.Scans.ActiveScanID()

	writeJSON(w, http.StatusOK, statusResponse{
		Service: "dinnerdecider",
		Version: versionOr(rt.deps.Build.Version),
		Commit:  rt.deps.Build.Commit,
		Built:   rt.deps.Build.BuildDate,
		Go:      runtime.Version(),
		Model:   statusModelValue,
		Hardware: statusHardware{
			GOARCH:           rt.deps.Target.GOARCH,
			Accelerator:      string(rt.deps.Target.Accelerator),
			AssetVariant:     rt.deps.Target.AssetVariant,
			HasOfficialBuild: rt.deps.Target.HasOfficialBuild(),
		},
		Memory:  rt.memoryStatus(),
		OCRMode: rt.ocrMode(),
		Auth:    string(rt.authMode()),
		Kroger:  rt.kroger.status(),
		Scan: statusScan{
			ActiveScanID: activeID,
			Running:      running,
			Scans:        len(rt.deps.Scans.List()),
			Subscribers:  rt.scan.sse.count(),
		},
		Limits: statusLimits{
			MaxRequestBytes:    rt.limits.MaxRequestBytes,
			MaxFileBytes:       rt.limits.MaxFileBytes,
			MaxFilesPerRequest: rt.limits.MaxFilesPerRequest,
			MaxUploadPixels:    rt.limits.MaxUploadPixels,
			MaxConcurrentScans: rt.limits.MaxConcurrentScans,
			MaxSSEPerScan:      rt.limits.MaxSSEPerScan,
			MaxSSETotal:        rt.limits.MaxSSETotal,
		},
		// The six phases, spelled out so a client can be written against the
		// closed set rather than against whatever it has happened to observe.
		// `failed` is in there, and it is the whole reason this list is worth
		// publishing: a client that only knows `empty` tells a user their
		// fridge looked empty when every model call actually threw.
		Phases: []contract.ScanPhase{
			contract.Idle, contract.Scanning, contract.Finished,
			contract.Empty, contract.Failed, contract.Cancelled,
		},
	})
}

// demoMode answers "is this real recognition?".
//
// Never silently present canned data as real recognition: if the model pair is
// not installed the answer is demo mode with a reason, and the UI shows an
// orange badge.
func (rt *Router) demoMode(installed modelInstalledStatus) (bool, string) {
	if !installed.Present {
		switch {
		case installed.WeightsFile == "" && installed.MmprojFile == "":
			return true, "The model files are not installed, so scans return sample data instead of real recognition."
		case installed.MmprojFile == "":
			return true, "The vision projector (mmproj) is missing, so the model cannot see images. Scans return sample data."
		default:
			return true, "The model weights are missing, so scans return sample data instead of real recognition."
		}
	}
	if rt.deps.Model == nil {
		return true, "This service is running without an inference backend, so scans return sample data."
	}
	if !rt.deps.Target.HasOfficialBuild() {
		return true, fmt.Sprintf("There is no official llama-server build for %s, so scans return sample data.", rt.deps.Target.GOARCH)
	}
	return false, ""
}

func (rt *Router) memoryStatus() statusMemory {
	status, err := rt.deps.Memory()
	if err != nil {
		return statusMemory{}
	}
	return statusMemory{
		Known:          true,
		TotalBytes:     status.TotalBytes,
		AvailableBytes: status.AvailableBytes,
		CanLoad:        status.CanLoad,
		Degraded:       status.Degraded,
		Message:        status.Message,
	}
}

func (rt *Router) ocrMode() string {
	if rt.deps.OCRMode != "" {
		return rt.deps.OCRMode
	}
	return "unknown"
}

func (rt *Router) authMode() config.AuthMode {
	if rt.deps.Auth != nil {
		return rt.deps.Auth.Mode()
	}
	if rt.deps.Config.Auth != "" {
		return rt.deps.Config.Auth
	}
	return config.AuthNone
}

func (rt *Router) scheme() string {
	if rt.deps.Config.TLSEnabled() {
		return "https"
	}
	return "http"
}

func (rt *Router) port() int {
	if rt.deps.Config.TLSEnabled() && rt.deps.Config.TLSPort > 0 {
		return rt.deps.Config.TLSPort
	}
	if rt.deps.Config.Port > 0 {
		return rt.deps.Config.Port
	}
	return config.DefaultPort
}

func versionOr(v string) string {
	if v == "" {
		return "dev"
	}
	return v
}
